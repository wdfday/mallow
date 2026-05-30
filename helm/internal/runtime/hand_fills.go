package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/infra/poslog"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/position"
)

// handleWsFill processes a fully-filled WsFillEvent received via the WS path.
func (h *Hand) handleWsFill(ctx context.Context, ev exchange.WsFillEvent) {
	h.mu.Lock()
	if _, seen := h.seenFills[ev.OrderID]; seen {
		// REST-immediate path already handled this fill — skip to avoid double-apply.
		h.mu.Unlock()
		return
	}
	h.seenFills[ev.OrderID] = struct{}{}
	h.mu.Unlock()

	side := "buy"
	if ev.Side == exchange.Sell {
		side = "sell"
	}
	qty := ev.FilledQty
	commission := ev.Commission
	// On Binance spot BUY, when the fee is paid in the base asset (e.g. "ETH" for ETHUSDT),
	// Binance deducts it from the received base qty. The wallet holds FilledQty - Commission,
	// not FilledQty. Record the net qty so exit orders don't exceed the actual balance.
	// Also normalize the commission to quote currency (commission × fill_price) so that
	// entry + exit fees can be summed consistently for the trade record.
	if side == "buy" && ev.Commission.IsPositive() && ev.CommissionAsset != "" &&
		strings.HasPrefix(ev.Symbol, ev.CommissionAsset) {
		qty = qty.Sub(ev.Commission)
		// Truncate to 4 decimal places so the qty fits exchange lot size step (0.0001).
		// Rounding down avoids insufficient-balance on exit; the dust is negligible.
		qty = qty.Truncate(4)
		// Convert base-asset fee → quote equivalent for consistent two-trip accounting.
		if ev.FilledAvg.IsPositive() {
			commission = ev.Commission.Mul(ev.FilledAvg)
		}
		slog.Debug("fill: base-asset fee deducted from qty",
			"hand_id", h.id, "symbol", ev.Symbol,
			"commission_asset", ev.CommissionAsset, "commission_base", ev.Commission,
			"commission_quote", commission, "gross_qty", ev.FilledQty, "net_qty", qty,
		)
	}
	h.applyFill(ctx, ev.OrderID, ev.Symbol, side, qty, ev.FilledAvg, commission, "ws")
}

// applyFill is the single authority for all fill side-effects:
// portfolio update, exit-level management, metrics, and poslog publishing.
// It is called from three paths:
//   - WS (handleWsFill): fast-path, fill arrives via broker WebSocket
//   - REST poll (pollOrders): 5s fallback when WS event was missed
//   - REST-immediate (handleSignal): broker confirmed fill in the PlaceOrder response
func (h *Hand) applyFill(ctx context.Context, orderID, symbol, side string, qty, price, commission decimal.Decimal, source string) {
	h.metrics.ordersFilled.Add(1)

	// ── 1. Resolve pending exit level (entry fill only) ───────────────────────
	// pendingExits holds the SL/TP for an entry order before the fill price is known.
	// On entry fill: promote to exitLevels (resolved to absolute price if IsOffset).
	// On close fill: pendingExits[orderID] will not exist — this block is a no-op.
	var resolvedEl exitLevel
	var hasExitBracket bool
	// offsetResolved flags an IsOffset entry whose absolute SL/TP we just computed.
	// We need to persist these back into LegState (via KindSLUpdated) after the leg
	// has transitioned to PhaseOpen, otherwise the leg's StopLoss/TakeProfit stay
	// zero — which corrupts LegSnapshot, the trade record's planned_risk, and
	// reconcile-on-restart.
	var offsetResolved bool
	// posIDForBracket: the leg's PositionID, captured here (before publishOrderFilled
	// deletes it from pendingOrderPos) and forwarded to the bracket goroutine so it
	// can write the KindBracketPlaced poslog event with the correct subject/positionID.
	var posIDForBracket string
	h.mu.Lock()
	posIDForBracket = h.pendingOrderPos[orderID]
	if pending, ok := h.pendingExits[orderID]; ok {
		delete(h.pendingExits, orderID)
		resolved := exitLevel{Side: pending.Side}
		if pending.IsOffset {
			// ── avg-anchor (pyramiding-design.md) ──
			// Anchor offsets to the leg's blended avg entry AFTER this add, NOT this fill's
			// price, so the SL/TP rebase tracks the merged cost (consistent with the avg-anchored
			// add gate in hand_runner.go). The leg state here is pre-fill, so compute the post-add
			// blended avg inline using the same qty/price publishOrderFilled feeds the leg → it
			// matches the leg's EntryPrice exactly. First entry: leg qty 0 → anchor == fill price.
			//   add 1 @ 100 (avg 100), offset -5 → SL = 95
			//   add 2 @ 110 (avg 105), offset -5 → SL = 100   ← anchored to blended avg, not last fill
			anchor := price
			if snap, ok := h.pos.LegSnapshot(posIDForBracket); ok && snap.Qty.IsPositive() {
				if newQty := snap.Qty.Add(qty); newQty.IsPositive() {
					anchor = snap.Qty.Mul(snap.EntryPrice).Add(qty.Mul(price)).Div(newQty)
				}
			}
			if anchor.IsPositive() {
				if !pending.StopOffset.IsZero() {
					resolved.StopLoss = anchor.Add(pending.StopOffset)
				}
				if !pending.TakeProfitOffset.IsZero() {
					resolved.TakeProfit = anchor.Add(pending.TakeProfitOffset)
				}
			}
			offsetResolved = true
		} else {
			resolved.StopLoss = pending.StopLoss
			resolved.TakeProfit = pending.TakeProfit
		}
		h.exitLevels[symbol] = resolved
		if resolved.StopLoss.IsPositive() || resolved.TakeProfit.IsPositive() {
			resolvedEl = resolved
			hasExitBracket = true
		}
	}
	h.mu.Unlock()

	// ── 2. Place exchange-side bracket orders (entry fill only) ──────────────
	// Runs in a goroutine so it doesn't block the fill handling path.
	// On success, stores the resulting order IDs back into exitLevels so they
	// can be cancelled if the position closes via the other exit leg.
	if hasExitBracket {
		if placer, ok := h.helmRuntime.Exchange.(exchange.ExitOrderPlacer); ok {
			exitSide := exchange.Sell
			if resolvedEl.Side == "sell" { // short → buy to close
				exitSide = exchange.Buy
			}
			market := exchange.MarketSpot
			if h.helmRuntime.Creds.AccountType == exchange.AccountFuturesUSDM ||
				h.helmRuntime.Creds.AccountType == exchange.AccountFuturesCOINM {
				market = exchange.MarketFutures
			}
			// Capture the hand's lifecycle context so the goroutine exits
			// immediately when Hand.Stop() is called — prevents goroutine leaks
			// when tests or operators shut down a hand during the retry window.
			h.mu.RLock()
			handCtx := h.ctx
			h.mu.RUnlock()

			go func(el exitLevel, posID string) {
				// Retry loop: spot exchanges may return "insufficient balance" briefly after
				// a fill if the asset has not yet settled into the available balance.
				// Retries up to 5× with linear backoff (1s, 2s … 5s = 15s total).
				exitReq := exchange.ExitOrderRequest{
					Symbol:     symbol,
					Market:     market,
					Side:       exitSide,
					Qty:        qty,
					StopLoss:   el.StopLoss,
					TakeProfit: el.TakeProfit,
				}
				var result *exchange.ExitOrderResult
				var err error
				for attempt := 1; attempt <= 5; attempt++ {
					select {
					case <-handCtx.Done():
						slog.Info("hand: exit order goroutine cancelled (hand stopped)", "hand_id", h.id, "symbol", symbol)
						return
					case <-time.After(time.Duration(attempt) * time.Second):
					}
					exitCtx, cancel := context.WithTimeout(handCtx, 10*time.Second)
					result, err = placer.PlaceExitOrders(exitCtx, h.helmRuntime.Creds, exitReq)
					cancel()
					if err == nil {
						break
					}
					slog.Warn("hand: place exit orders retry", "hand_id", h.id, "symbol", symbol,
						"attempt", attempt, "err", err)
				}
				if err != nil {
					slog.Error("hand: place exit orders failed", "hand_id", h.id, "symbol", symbol, "err", err)
					return
				}
				slog.Info("hand: exit orders placed", "hand_id", h.id, "symbol", symbol, "order_ids", result.OrderIDs)
				h.helmRuntime.EmitEvent(natsapi.HelmEvent{
					HandID:  h.id.String(),
					Code:    CodeOrderExitPlaced,
					Symbol:  symbol,
					OrderID: fmt.Sprintf("%v", result.OrderIDs),
					Reason:  fmt.Sprintf("stop_loss=%s take_profit=%s", el.StopLoss, el.TakeProfit),
					Msg:     "order: bracket exit placed (safety net)",
				})
				for _, id := range result.OrderIDs {
					h.trackOrder(id)
				}
				h.mu.Lock()
				if lv, ok := h.exitLevels[symbol]; ok {
					lv.ExchangeOrderIDs = result.OrderIDs
					h.exitLevels[symbol] = lv
				}
				h.mu.Unlock()
				// Persist bracket order IDs to poslog so they survive a restart.
				if posID != "" {
					h.publishBracketPlaced(handCtx, posID, symbol, result.OrderIDs)
				}
			}(resolvedEl, posIDForBracket)
		}
	}

	// ── 3. Close detection via poslog phase ──────────────────────────────────
	// Use h.pos (poslog-backed per-hand state) instead of rt.Portfolio.
	// h.pos still reflects pre-fill state here — publishOrderFilled runs later.
	// A close fill is identified by the leg being in PhaseExiting (exit order placed
	// and now confirmed filled), as opposed to PhaseEntering (entry order).
	h.mu.RLock()
	posID := h.pendingOrderPos[orderID]
	isClosingFill := posID != "" && h.pos.LegPhase(posID) == position.PhaseExiting
	entryPrice := h.pos.LegEntryPrice(posID)

	var isBracketExit bool
	if !isClosingFill {
		if lv, ok := h.exitLevels[symbol]; ok {
			for _, id := range lv.ExchangeOrderIDs {
				if id == orderID {
					isBracketExit = true
					isClosingFill = true
					if primaryLeg := h.pos.PrimaryLeg(); primaryLeg != nil {
						posID = primaryLeg.PositionID
						entryPrice = primaryLeg.EntryPrice
					}
					break
				}
			}
		}
	}
	h.mu.RUnlock()

	var closePnL decimal.Decimal
	var closeSource string
	if isClosingFill {
		h.mu.Lock()
		h.cancelExitOrders(ctx, symbol, orderID)
		delete(h.exitLevels, symbol)
		h.mu.Unlock()
		if isBracketExit {
			closeSource = "bracket_exit"
		} else {
			closeSource = source
		}
		if price.IsPositive() && entryPrice.IsPositive() {
			if side == "sell" { // closing a long position
				closePnL = price.Sub(entryPrice).Mul(qty)
			} else { // closing a short position (buy to cover)
				closePnL = entryPrice.Sub(price).Mul(qty)
			}
			h.metrics.mu.Lock()
			h.metrics.totalPnL = h.metrics.totalPnL.Add(closePnL)
			h.metrics.totalCommission = h.metrics.totalCommission.Add(commission)
			if closePnL.IsPositive() {
				h.metrics.winCount++
			} else {
				h.metrics.lossCount++
			}
			h.metrics.mu.Unlock()
			h.checkEdgeRisk(closePnL)
		}
	}

	// ── 4. Portfolio update ───────────────────────────────────────────────────
	// Single update point for all fill paths (WS, poll, REST-immediate).
	// Registry no longer calls ReportFill before routing to hand.
	h.helmRuntime.ReportFill(helmdomain.FillReport{
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		OrderID:    orderID,
		Symbol:     symbol,
		Side:       side,
		Qty:        qty,
		Price:      price,
		Commission: commission,
		Timestamp:  time.Now().UTC(),
	})

	// ── 5. Poslog ─────────────────────────────────────────────────────────────
	// publishOrderFilled updates h.pos (ActiveLegs) — must run BEFORE emitEvent
	// so that observers (tests, SSE clients) see a consistent DeployedCapital.
	h.publishOrderFilled(ctx, orderID, qty, price, closePnL, commission, source, closeSource)

	// ── 5.5. Persist resolved IsOffset SL/TP into the leg ───────────────────
	// For offset entries the OrderPlaced poslog carried zero SL/TP (fill price
	// unknown at placement). Now that we've resolved absolute levels, emit
	// KindSLUpdated so:
	//   1. LegState.{StopLoss,TakeProfit} reflect the absolute prices — picked up by
	//      LegSnapshot → PositionClosedPayload → trade record's planned_risk + r_multiple.
	//   2. Poslog replay on restart restores the resolved levels independently of
	//      whether the bracket-order goroutine succeeded.
	// Must run AFTER publishOrderFilled so the leg is in PhaseOpen (applySLUpdated requires it).
	if offsetResolved && !isClosingFill && posID != "" &&
		(resolvedEl.StopLoss.IsPositive() || resolvedEl.TakeProfit.IsPositive()) {
		var newSL, newTP string
		if resolvedEl.StopLoss.IsPositive() {
			newSL = resolvedEl.StopLoss.String()
		}
		if resolvedEl.TakeProfit.IsPositive() {
			newTP = resolvedEl.TakeProfit.String()
		}
		payload, _ := json.Marshal(poslog.SLUpdatedPayload{
			OrderID: posID,
			NewSL:   newSL,
			NewTP:   newTP,
			Reason:  "offset_resolved",
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:         posID + "_sl_resolved_" + orderID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: posID,
			Kind:       poslog.KindSLUpdated,
			Payload:    payload,
			At:         time.Now().UTC(),
		})
	}

	// ── 6. REST-path fill publish ─────────────────────────────────────────────
	// source="ws"  → registry_fills.go already published with real exchange TradeID.
	// source="rest" → WS echo will arrive ~100ms later and publish with real TradeID.
	//                 If WS doesn't arrive, helm_sync.go picks it up (same real TradeID
	//                 → JetStream dedup by Nats-Msg-Id catches the sync re-publish).
	//                 So we skip publish here to avoid the synthetic orderID "ack" event.
	// other sources (poll, kill, …) → no WS echo expected; publish now so consumers
	//                 get near-realtime fills. MarkOrderFillPublished blocks Sync() re-publish.
	if source != "ws" {
		h.helmRuntime.RemoveOrderTracking(orderID)
	}
	if source != "ws" && source != "rest" {
		if h.helmRuntime.js != nil {
			natsapi.PublishTradeFill(h.helmRuntime.js, natsapi.TransactionMsg{
				HelmID:    h.helmID.String(),
				AccountID: h.helmRuntime.AccountID.String(),
				UserID:    h.helmRuntime.UserID.String(),
				HandID:    h.id.String(),
				TradeID:   "", // no exchange trade ID on poll/timeout paths; dedup falls back to helmID+orderID
				OrderID:   orderID,
				Kind:      "fill",
				Symbol:    symbol,
				Side:      side,
				Qty:       qty,
				AvgPrice:  price,
				FilledAt:  time.Now().UTC(),
			})
			h.helmRuntime.MarkOrderFillPublished(orderID)
		}
	}

	equity := h.realizedEquity()
	deployed := h.DeployedCapital()
	// Compute unrealized PnL across all open legs using last known price.
	var unrealized decimal.Decimal
	h.mu.RLock()
	for _, leg := range h.pos.ActiveLegs() {
		curPrice := h.helmRuntime.lastKnownPrice(leg.Symbol)
		if curPrice.IsPositive() && leg.EntryPrice.IsPositive() {
			if leg.Side == "buy" {
				unrealized = unrealized.Add(curPrice.Sub(leg.EntryPrice).Mul(leg.Qty.Abs()))
			} else {
				unrealized = unrealized.Add(leg.EntryPrice.Sub(curPrice).Mul(leg.Qty.Abs()))
			}
		}
	}
	h.mu.RUnlock()
	availCash := equity.Sub(deployed) // USDT liquid — not locked in open positions
	slog.Info("order: filled",
		"hand_id", h.id,
		"symbol", symbol,
		"side", side,
		"qty", qty,
		"price", price,
		"order_id", orderID,
		"source", source,
		"pnl", closePnL,
		"available_cash", availCash,
		"deployed", deployed,
		"unrealized_pnl", unrealized,
		"equity", equity,
	)
	h.emitEvent(natsapi.HelmEvent{
		Code:            CodeOrderFilled,
		Symbol:          symbol,
		Side:            side,
		Qty:             qty,
		Price:           price,
		OrderID:         orderID,
		Reason:          source,
		Msg:             "order: filled",
		PnL:             closePnL,
		AvailableCash:   availCash,
		DeployedCapital: deployed,
		Equity:          equity,
	})

}

// checkEdgeRisk evaluates the per-hand sliding-window edge-degradation guard.
// Called after every closing fill from the run-loop goroutine (no extra lock needed
// for the ring buffer fields — they are exclusively written here).
// Auto-pauses the hand when any enabled threshold is breached.
func (h *Hand) checkEdgeRisk(pnl decimal.Decimal) {
	cfg := h.edgeRisk
	if cfg.WindowTrades == 0 {
		return
	}

	// ── 1. Push PnL into ring ─────────────────────────────────────────────────
	h.tradeRing[h.ringHead] = pnl
	h.ringHead = (h.ringHead + 1) % cfg.WindowTrades
	if h.ringHead == 0 {
		h.ringFull = true
	}

	// ── 2. Update consecutive-loss streak ────────────────────────────────────
	if pnl.IsNegative() {
		h.consecLoss++
	} else {
		h.consecLoss = 0
	}

	// ── 3. Determine active window size ──────────────────────────────────────
	count := cfg.WindowTrades
	if !h.ringFull {
		count = h.ringHead // number of pushes so far; ringHead hasn't wrapped yet
		if count == 0 {
			return
		}
	}

	// ── 4. Compute window stats ───────────────────────────────────────────────
	sum := decimal.Zero
	minPnL := h.tradeRing[0]
	for i := 0; i < count; i++ {
		sum = sum.Add(h.tradeRing[i])
		if h.tradeRing[i].LessThan(minPnL) {
			minPnL = h.tradeRing[i]
		}
	}
	avg := sum.Div(decimal.NewFromInt(int64(count)))

	// Reference capital for pct thresholds.
	h.mu.RLock()
	ref := h.allocatedCap
	h.mu.RUnlock()
	if !ref.IsPositive() {
		ref = h.helmRuntime.Portfolio.Equity()
	}
	if !ref.IsPositive() {
		return
	}

	// ── 5. Check thresholds ───────────────────────────────────────────────────
	pct := func(v decimal.Decimal) float64 {
		return v.Div(ref).Mul(decimal.NewFromInt(100)).InexactFloat64()
	}

	var breachReason string
	switch {
	case cfg.MaxTotalLossPct > 0 && sum.LessThan(ref.Mul(decimal.NewFromFloat(-cfg.MaxTotalLossPct))):
		breachReason = fmt.Sprintf("window total loss %.2f%% > max %.2f%% (last %d trades)",
			-pct(sum), cfg.MaxTotalLossPct*100, count)

	case cfg.MaxAvgLossPct > 0 && avg.LessThan(ref.Mul(decimal.NewFromFloat(-cfg.MaxAvgLossPct))):
		breachReason = fmt.Sprintf("window avg loss %.2f%% > max %.2f%% (last %d trades)",
			-pct(avg), cfg.MaxAvgLossPct*100, count)

	case cfg.MaxSingleLossPct > 0 && minPnL.LessThan(ref.Mul(decimal.NewFromFloat(-cfg.MaxSingleLossPct))):
		breachReason = fmt.Sprintf("single trade loss %.2f%% > max %.2f%%",
			-pct(minPnL), cfg.MaxSingleLossPct*100)

	case cfg.MaxConsecLoss > 0 && h.consecLoss >= cfg.MaxConsecLoss:
		breachReason = fmt.Sprintf("consecutive losses %d reached max %d", h.consecLoss, cfg.MaxConsecLoss)
	}

	if breachReason == "" {
		return
	}

	// ── 6. Auto-stop ──────────────────────────────────────────────────────────
	slog.Warn("hand: edge degradation detected — auto-stopping",
		"hand_id", h.id, "reason", breachReason)
	h.emitEvent(natsapi.HelmEvent{
		Code:   CodeHandAutoStopped,
		Reason: "edge risk: " + breachReason,
		Msg:    "hand: auto-stopped — edge degradation",
	})
	go h.Stop()
}
