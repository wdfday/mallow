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
func (h *Hand) applyFill(ctx context.Context, orderID, symbol, side string,
	qty, price, commission decimal.Decimal, source string) {

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
	// bracketQty is the qty the exchange bracket must cover: the leg's MERGED total after
	// this add (pre-add leg qty + this fill), not just this add's qty — otherwise a pyramid
	// add would leave a bracket covering only the latest leg.
	bracketQty := qty
	h.mu.Lock()
	posIDForBracket = h.pendingOrderPos[orderID]
	if pending, ok := h.pendingExits[orderID]; ok {
		delete(h.pendingExits, orderID)

		// Pre-add leg state (LegSnapshot is pre-fill — this fill is applied later via
		// publishOrderFilled). Drives both the blended-avg anchor and the merged bracket qty.
		var preQty, preAvg decimal.Decimal
		if snap, ok := h.pos.LegSnapshot(posIDForBracket); ok && snap.Qty.IsPositive() {
			preQty, preAvg = snap.Qty, snap.EntryPrice
			bracketQty = preQty.Add(qty) // merged total
		}

		resolved := exitLevel{Side: pending.Side}
		if pending.IsOffset {
			// ── avg-anchor (pyramiding-design.md) ──
			// Anchor offsets to the leg's blended avg entry AFTER this add, NOT this fill's
			// price, so the SL/TP track the merged cost (consistent with the avg-anchored add
			// gate in hand_runner.go). First entry: preQty 0 → anchor == fill price.
			//   add 1 @ 100 (avg 100), offset -5 → SL = 95
			//   add 2 @ 110 (avg 105), offset -5 → SL = 100   ← anchored to blended avg, not last fill
			anchor := price
			if preQty.IsPositive() {
				if newQty := preQty.Add(qty); newQty.IsPositive() {
					anchor = preQty.Mul(preAvg).Add(qty.Mul(price)).Div(newQty)
				}
			}
			if anchor.IsPositive() {
				// Round to the exchange tick size (authoritative) so SL/TP satisfy
				// PRICE_FILTER. StopOffset/TakeProfitOffset come from Rust float
				// arithmetic and carry many decimal places; fill price representation
				// is also unreliable (Binance may return "2014.1200000" → Exponent=-7).
				prec := priceTick(h.helmRuntime.filtersFor(ctx, symbol).PriceTick)
				if !pending.StopOffset.IsZero() {
					resolved.StopLoss = anchor.Add(pending.StopOffset).Round(prec)
				}
				if !pending.TakeProfitOffset.IsZero() {
					resolved.TakeProfit = anchor.Add(pending.TakeProfitOffset).Round(prec)
				}
			}
			offsetResolved = true
		} else {
			// Absolute SL/TP from signal — Rust float, round to exchange tick.
			prec := priceTick(h.helmRuntime.filtersFor(ctx, symbol).PriceTick)
			if pending.StopLoss.IsPositive() {
				resolved.StopLoss = pending.StopLoss.Round(prec)
			}
			if pending.TakeProfit.IsPositive() {
				resolved.TakeProfit = pending.TakeProfit.Round(prec)
			}
		}

		// Pyramid add: cancel the prior consolidated bracket BEFORE overwriting exitLevels.
		// Otherwise the old exchange order is orphaned (its IDs are about to be lost) and the
		// position ends up with split partial brackets at stale levels. The new bracket below
		// is placed for the full merged qty at the rebased level — one bracket per position.
		// (Marks pendingCancels so HandleExitOrderCanceled treats this as helm cleanup, not a
		// disown.) Caller holds h.mu, as cancelExitOrders requires.
		if old, ok := h.exitLevels[symbol]; ok && len(old.ExchangeOrderIDs) > 0 {
			h.cancelExitOrders(ctx, symbol, "")
		}

		h.exitLevels[symbol] = resolved
		if resolved.StopLoss.IsPositive() || resolved.TakeProfit.IsPositive() {
			// Sanity check: for a long exit (sell), SL must be below TP; for short (buy), above.
			// Rounding can collapse SL and TP to the same value — sending that to the exchange
			// causes a hard error ("relationship of prices not correct"). Skip OCO in that case.
			slTpOK := true
			if resolved.StopLoss.IsPositive() && resolved.TakeProfit.IsPositive() {
				if resolved.Side == "buy" { // short → TP < SL
					slTpOK = resolved.TakeProfit.LessThan(resolved.StopLoss)
				} else { // long → SL < TP
					slTpOK = resolved.StopLoss.LessThan(resolved.TakeProfit)
				}
				if !slTpOK {
					slog.Warn("fill: SL/TP collapsed to same value after rounding — skipping OCO bracket",
						"hand_id", h.id, "symbol", symbol,
						"stop_loss", resolved.StopLoss, "take_profit", resolved.TakeProfit)
				}
			}
			if slTpOK {
				resolvedEl = resolved
				hasExitBracket = true
			}
		}
	}
	h.mu.Unlock()

	// ── 2. Place exchange-side bracket orders (entry fill only) ──────────────
	// Runs in a goroutine so it doesn't block the fill handling path.
	// On success, stores the resulting order IDs back into exitLevels so they
	// can be canceled if the position closes via the other exit leg.
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
				bktCtx := handCtx
				exitReq := exchange.ExitOrderRequest{
					Symbol:     symbol,
					Market:     market,
					Side:       exitSide,
					Qty:        truncateQty(h.helmRuntime.filtersFor(bktCtx, symbol), bracketQty),
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
					// Exchange-side bracket failed after all retries. The position is NOT
					// unprotected: exitLevels[symbol] still holds the SL/TP with an EMPTY
					// ExchangeOrderIDs, so the in-process checkExits monitor is automatically
					// the active net (it fires a market close when price crosses, polled ≤5s).
					// But the exchange-side guarantee is gone — escalate loudly so a human
					// knows this position relies on the helm process staying up.
					slog.Error("hand: place exit orders failed — position now relies on the local monitor only",
						"hand_id", h.id, "symbol", symbol, "err", err)
					h.helmRuntime.EmitEvent(natsapi.HelmEvent{
						HandID: h.id.String(),
						Code:   CodeOrderExitFailed,
						Symbol: symbol,
						Reason: fmt.Sprintf("qty=%s stop_loss=%s take_profit=%s err=%s", exitReq.Qty, el.StopLoss, el.TakeProfit, err),
						Msg:    "order: exchange SL/TP bracket FAILED — local monitor is the only net",
					})
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
	// legQty is the full position size before this fill is applied.
	// Used for dust detection (step 4.5) and the trade record (publishOrderFilled).
	var legQty decimal.Decimal
	if isClosingFill {
		if snap, ok := h.pos.LegSnapshot(posID); ok {
			legQty = snap.Qty.Abs()
		}
	}

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
						legQty = primaryLeg.Qty.Abs()
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
			// dust_exit is a synthetic rounding fill — same trade, not a new trade event.
			// Accumulate PnL for accurate totals, but skip win/loss count and edge risk:
			// the "trade outcome" was already decided and recorded by the main closing fill.
			if source != "dust_exit" {
				if closePnL.IsPositive() {
					h.metrics.winCount++
				} else {
					h.metrics.lossCount++
				}
			}
			h.metrics.mu.Unlock()
			if source != "dust_exit" {
				h.checkEdgeRisk(closePnL)
			}
		}
	}

	// ── 4. Portfolio update (main exchange fill) ─────────────────────────────
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

	// ── 4.5. Dust reconciliation (spot closing fills only) ────────────────────
	// When a spot exit is truncated to the exchange's LOT_SIZE step, the remaining
	// sub-step qty (dust) cannot be sold at the exchange. The hand "sells" the
	// dust internally to helm at the current market price so that:
	//   1. Helm's portfolio is credited with the USDT value of the dust.
	//   2. The dust PnL is included in the trade's gross PnL (via legQty in
	//      publishOrderFilled → appendTradeRecord).
	//   3. dustLedger suppresses a false checkPositionDesync alarm for the
	//      tiny physical BTC residual that remains at the exchange.
	//
	// MUST run BEFORE publishOrderFilled: the leg still exists in h.pos here.
	// After publishOrderFilled emits KindPositionClosed, the leg is gone.
	isFutures := h.helmRuntime.Creds.AccountType == exchange.AccountFuturesUSDM ||
		h.helmRuntime.Creds.AccountType == exchange.AccountFuturesCOINM
	if isClosingFill && !isFutures && legQty.IsPositive() {
		dust := legQty.Sub(qty)
		filters := h.helmRuntime.filtersFor(ctx, symbol)
		if filters.QtyStep.IsPositive() && dust.IsPositive() && dust.LessThan(filters.QtyStep) {
			dustPrice := price
			if mp := h.helmRuntime.lastKnownPrice(symbol); mp.IsPositive() {
				dustPrice = mp
			}
			slog.Info("hand: dust reconciliation — sub-step residual returned to helm at market price",
				"hand_id", h.id, "symbol", symbol,
				"dust", dust, "price", dustPrice, "step", filters.QtyStep,
			)
			// Credit portfolio with the USDT value of the dust.
			h.helmRuntime.ReportFill(helmdomain.FillReport{
				HandID:    h.id.String(),
				HelmID:    h.helmID.String(),
				OrderID:   orderID + "_dust",
				Symbol:    symbol,
				Side:      side,
				Qty:       dust,
				Price:     dustPrice,
				Timestamp: time.Now().UTC(),
			})
			// Accumulate dust PnL into hand metrics (no win/loss count — same trade).
			if entryPrice.IsPositive() {
				var dustPnL decimal.Decimal
				if side == "sell" {
					dustPnL = dustPrice.Sub(entryPrice).Mul(dust)
				} else {
					dustPnL = entryPrice.Sub(dustPrice).Mul(dust)
				}
				h.metrics.mu.Lock()
				h.metrics.totalPnL = h.metrics.totalPnL.Add(dustPnL)
				h.metrics.mu.Unlock()
			}
			// Suppress checkPositionDesync for the physical BTC residual at exchange.
			h.helmRuntime.RecordDust(symbol, dust)
		}
	}

	// ── 5. poslog ─────────────────────────────────────────────────────────────
	// publishOrderFilled updates h.pos (ActiveLegs) — must run BEFORE emitEvent
	// so that observers (tests, SSE clients) see a consistent DeployedCapital.
	// deployedCapital = quote cost of THIS fill = qty×price + entry_fee_quote.
	// Zero for exit fills (no new capital is deployed on close).
	var deployedCapital decimal.Decimal
	if !isClosingFill {
		deployedCapital = qty.Mul(price).Add(commission)
	}
	// Pass legQty so publishOrderFilled uses the full position size in the trade
	// record (gross_pnl = legQty×exitPrice − legQty×entryPrice, which includes
	// the dust portion at approximately the same price).
	h.publishOrderFilled(ctx, orderID, qty, legQty, price, closePnL, commission, deployedCapital, source, closeSource)

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
	cfg := h.guard.cfg
	if cfg.WindowTrades == 0 {
		return
	}

	// ── 1. Push PnL into ring ─────────────────────────────────────────────────
	h.guard.push(pnl)

	// ── 2. Update consecutive-loss streak ────────────────────────────────────
	if pnl.IsNegative() {
		h.guard.consecLoss++
	} else {
		h.guard.consecLoss = 0
	}

	// ── 3. Determine active window size ──────────────────────────────────────
	count := cfg.WindowTrades
	if !h.guard.full {
		count = h.guard.head // number of pushes so far; head hasn't wrapped yet
		if count == 0 {
			return
		}
	}

	// ── 4. Compute window stats ───────────────────────────────────────────────
	sum := decimal.Zero
	minPnL := h.guard.ring[0]
	for i := 0; i < count; i++ {
		sum = sum.Add(h.guard.ring[i])
		if h.guard.ring[i].LessThan(minPnL) {
			minPnL = h.guard.ring[i]
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

	case cfg.MaxConsecLoss > 0 && h.guard.consecLoss >= cfg.MaxConsecLoss:
		breachReason = fmt.Sprintf("consecutive losses %d reached max %d", h.guard.consecLoss, cfg.MaxConsecLoss)
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
