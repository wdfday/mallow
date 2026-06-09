package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/infra/poslog"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/position"
	"mallow/helm/internal/safe"
)

// handleWsFill processes a fully-filled WsFillEvent received via the WS path.
func (h *Hand) handleWsFill(ctx context.Context, ev exchange.WsFillEvent) {
	h.mu.Lock()
	if _, seen := h.seenFills[ev.OrderID]; seen {
		// REST-immediate path already handled this fill — skip to avoid double-apply.
		h.mu.Unlock()
		return
	}
	h.seenFills[ev.OrderID] = time.Now()
	h.mu.Unlock()

	side := "buy"
	if ev.Side == exchange.Sell {
		side = "sell"
	}
	h.applyFill(ctx, ev.OrderID, ev.Symbol, side, ev.FilledQty, ev.FilledAvg, ev.Commission, "ws")
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

	h.mu.Lock()
	state := h.partialApplied[orderID]
	delete(h.partialApplied, orderID)
	h.mu.Unlock()

	cumulativeQty := qty.Add(state.Qty)
	cumulativeCommission := commission.Add(state.Commission)
	cumulativeAvgPrice := price
	if cumulativeQty.IsPositive() {
		cumulativeCost := qty.Mul(price).Add(state.Cost)
		cumulativeAvgPrice = cumulativeCost.Div(cumulativeQty)
	}

	// ── 1. Resolve pending exit level (entry fill only) ───────────────────────
	// pendingExits holds the SL/TP for an entry order before the fill price is known.
	// On entry fill: promote to exitLevels (resolved to absolute price if IsOffset).
	// On close fill: pendingExits[orderID] will not exist — this block is a no-op.
	var resolvedEl exitLevel
	var hasExitBracket bool
	// oldBracketIDs holds the exchange OCO order IDs that existed before a pyramid add fill.
	// Populated inside the pendingExits block below; used after the lock to emit a
	// bracket-replaced event explaining why the old OCO was cancelled.
	var oldBracketIDs []string
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
	bracketQty := cumulativeQty
	h.mu.Lock()
	posIDForBracket = h.pendingOrderPos[orderID]
	if pending, ok := h.pendingExits[orderID]; ok {
		delete(h.pendingExits, orderID)

		// Pre-add leg state (LegSnapshot is pre-fill — this fill is applied later via
		// publishOrderFilled). Drives both the blended-avg anchor and the merged bracket qty.
		var preQty, preAvg decimal.Decimal
		if snap, ok := h.pos.LegSnapshot(posIDForBracket); ok && snap.Qty.IsPositive() {
			preQty, preAvg = snap.Qty, snap.EntryPrice
			bracketQty = preQty.Add(cumulativeQty) // merged total
		}

		// Capture existing bracket IDs BEFORE resolvePendingExit cancels them.
		// Used after the lock is released to emit a bracket-replaced event.
		if preQty.IsPositive() {
			if old, ok := h.exitLevels[symbol]; ok {
				oldBracketIDs = append([]string(nil), old.ExchangeOrderIDs...)
			}
		}

		resolvedEl, hasExitBracket, offsetResolved = h.resolvePendingExit(ctx, symbol, pending, cumulativeAvgPrice, cumulativeQty, preQty, preAvg)
	}
	h.mu.Unlock()

	// ── 1.5. Bracket replacement event (pyramid add only) ────────────────────
	// When a pyramid add fills, resolvePendingExit cancels the old OCO bracket and
	// schedules a new one covering the merged position. Emit an explicit event so
	// users understand why an OCO bracket was cancelled — it is not an external close.
	if len(oldBracketIDs) > 0 && hasExitBracket {
		h.emitEvent(natsapi.HelmEvent{
			Code:    CodeOrderCancelled,
			Symbol:  symbol,
			OrderID: fmt.Sprintf("%v", oldBracketIDs),
			Reason:  "pyramid_add_bracket_replace",
			Msg:     "order: OCO bracket cancelled — pyramid add will place new merged bracket",
		})
	}

	// ── 2. Place exchange-side bracket orders (entry fill only) ──────────────
	// Runs in a goroutine so it doesn't block the fill handling path.
	// On success, stores the resulting order IDs back into exitLevels so they
	// can be canceled if the position closes via the other exit leg.
	if hasExitBracket {
		h.scheduleBracketPlacement(resolvedEl, symbol, bracketQty, posIDForBracket)
	}

	// ── 3. Close detection via poslog phase ──────────────────────────────────
	// Use h.pos (poslog-backed per-hand state) instead of rt.Portfolio.
	// h.pos still reflects pre-fill state here — publishOrderFilled runs later.
	// A close fill is identified by the leg being in PhaseExiting (exit order placed
	// and now confirmed filled), as opposed to PhaseEntering (entry order).
	h.mu.RLock()
	posID := h.pendingOrderPos[orderID]
	isClosingFill := posID != "" && h.pos.LegPhase(posID) == position.PhaseExiting
	preFillPhase := h.pos.LegPhase(posID) // captured before publishOrderFilled mutates leg state
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
		// Read exitLevels BEFORE cancelling — needed to classify bracket exit as sl/tp.
		// NOTE: do NOT delete exitLevels[symbol] here. publishOrderFilled (called at
		// step 5 below) needs exitLevels[symbol] intact to detect isBracketExit and
		// find the positionID for the position-closed event + trade record.
		// The delete is deferred to after publishOrderFilled.
		el := h.exitLevels[symbol]
		h.cancelExitOrders(ctx, symbol, orderID)
		h.mu.Unlock()
		switch {
		case isBracketExit:
			// Determine whether the SL or TP leg of the OCO filled by comparing
			// the fill price to the saved levels. Side == "buy" means entry was
			// a long (exit sell): SL is below entry, TP is above entry.
			//
			// Primary: strict directional check (fill clearly past the level).
			// Fallback: proximity — when the exchange uses market-price execution
			// (e.g. OKX tpOrdPx=-1), the actual fill can be 1-2 ticks inside the
			// trigger (64.40 fill on a 64.41 TP trigger). Strict check fails; pick
			// whichever level the fill is closest to. This is safe because a real
			// TP fill is always closer to TP than to SL, and vice versa.
			closeSource = "bracket_exit" // fallback if levels unavailable
			if el.StopLoss.IsPositive() && el.TakeProfit.IsPositive() {
				if el.Side == "buy" { // long: SL below entry, TP above entry
					if cumulativeAvgPrice.LessThanOrEqual(el.StopLoss) {
						closeSource = "sl"
					} else if cumulativeAvgPrice.GreaterThanOrEqual(el.TakeProfit) {
						closeSource = "tp"
					} else {
						// Market-execution slippage: classify by proximity.
						distSL := cumulativeAvgPrice.Sub(el.StopLoss).Abs()
						distTP := cumulativeAvgPrice.Sub(el.TakeProfit).Abs()
						if distTP.LessThanOrEqual(distSL) {
							closeSource = "tp"
						} else {
							closeSource = "sl"
						}
					}
				} else { // short: TP below entry, SL above entry
					if cumulativeAvgPrice.GreaterThanOrEqual(el.StopLoss) {
						closeSource = "sl"
					} else if cumulativeAvgPrice.LessThanOrEqual(el.TakeProfit) {
						closeSource = "tp"
					} else {
						distSL := cumulativeAvgPrice.Sub(el.StopLoss).Abs()
						distTP := cumulativeAvgPrice.Sub(el.TakeProfit).Abs()
						if distTP.LessThanOrEqual(distSL) {
							closeSource = "tp"
						} else {
							closeSource = "sl"
						}
					}
				}
			}
			switch closeSource {
			case "tp":
				slog.Info("hand: TP triggered — take-profit bracket filled",
					"hand_id", h.id, "symbol", symbol, "order_id", orderID,
					"fill_price", cumulativeAvgPrice, "tp_level", el.TakeProfit)
			case "sl":
				slog.Info("hand: SL triggered — stop-loss bracket filled",
					"hand_id", h.id, "symbol", symbol, "order_id", orderID,
					"fill_price", cumulativeAvgPrice, "sl_level", el.StopLoss)
			default:
				slog.Info("hand: bracket exit filled (TP/SL level unknown)",
					"hand_id", h.id, "symbol", symbol, "order_id", orderID,
					"fill_price", cumulativeAvgPrice,
					"sl_level", el.StopLoss, "tp_level", el.TakeProfit)
			}
		case source == "kill":
			closeSource = "kill"
		case source == "release":
			closeSource = "release"
		case source == "dust_exit":
			closeSource = "dust"
		default:
			// rest / ws / poll / partial_cancel — all mean a strategy DirExit
			// signal or local exit monitor triggered the closing order.
			closeSource = "signal"
		}
		if cumulativeAvgPrice.IsPositive() && entryPrice.IsPositive() {
			if side == "sell" { // closing a long position
				closePnL = cumulativeAvgPrice.Sub(entryPrice).Mul(cumulativeQty)
			} else { // closing a short position (buy to cover)
				closePnL = entryPrice.Sub(cumulativeAvgPrice).Mul(cumulativeQty)
			}
			h.metrics.mu.Lock()
			h.metrics.totalPnL = h.metrics.totalPnL.Add(closePnL)
			h.metrics.totalCommission = h.metrics.totalCommission.Add(cumulativeCommission)
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
		dust := legQty.Sub(cumulativeQty)
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
		}
	}

	// ── 5. poslog ─────────────────────────────────────────────────────────────
	// publishOrderFilled updates h.pos (ActiveLegs) — must run BEFORE emitEvent
	// so that observers (tests, SSE clients) see a consistent DeployedCapital.
	// deployedCapital = quote cost of THIS fill = qty×price + entry_fee_quote.
	// Zero for exit fills (no new capital is deployed on close).
	var deployedCapital decimal.Decimal
	if !isClosingFill {
		deployedCapital = cumulativeQty.Mul(cumulativeAvgPrice).Add(cumulativeCommission)
		h.metrics.mu.Lock()
		h.metrics.totalCommission = h.metrics.totalCommission.Add(cumulativeCommission)
		h.metrics.mu.Unlock()
	}
	// Pass legQty so publishOrderFilled uses the full position size in the trade
	// record (gross_pnl = legQty×exitPrice − legQty×entryPrice, which includes
	// the dust portion at approximately the same price).
	h.publishOrderFilled(ctx, orderID, cumulativeQty, legQty, cumulativeAvgPrice, closePnL, cumulativeCommission, deployedCapital, source, closeSource)

	// Delete exitLevels[symbol] AFTER publishOrderFilled so that publishOrderFilled
	// can detect isBracketExit by finding the bracket order ID in exitLevels.
	// cancelExitOrders already ran above (marks sibling in pendingCancels and
	// launches a REST cancel goroutine) — it only needs exitLevels to exist during
	// its own execution, not during publishOrderFilled.
	if isClosingFill {
		h.mu.Lock()
		delete(h.exitLevels, symbol)
		h.mu.Unlock()
	}

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

	// ── 6.5. Position lifecycle events ───────────────────────────────────────
	// Emitted after CodeOrderFilled so the order event (low-level fact) always
	// precedes the position event (higher-level view) in the activity feed.
	// preFillPhase was captured before publishOrderFilled mutated leg state —
	// it reflects the leg's intent (entering vs adding vs exiting) at fill time.
	switch {
	case isClosingFill:
		// Position closed: carry realized PnL + pct so the UI can display the trade outcome.
		// pnl_pct is a fraction (FE ×100 to show %). entryPrice×legQty = notional deployed.
		var pnlPct decimal.Decimal
		if entryPrice.IsPositive() && legQty.IsPositive() {
			pnlPct = closePnL.Div(entryPrice.Mul(legQty))
		}
		h.emitEvent(natsapi.HelmEvent{
			Code:            CodePositionClosed,
			Symbol:          symbol,
			Side:            side,
			Qty:             cumulativeQty,
			Price:           cumulativeAvgPrice,
			PositionID:      posID,
			EntryPrice:      entryPrice,
			PnL:             closePnL,
			PnLPct:          pnlPct,
			Reason:          closeSource,
			Msg:             "position: closed",
			AvailableCash:   availCash,
			DeployedCapital: deployed,
			Equity:          equity,
		})
	case preFillPhase == position.PhaseAdding:
		// Pyramid add confirmed — show total position qty and new blended avg entry.
		var newAvgEntry, totalQty decimal.Decimal
		h.mu.RLock()
		if snap, ok := h.pos.LegSnapshot(posID); ok {
			newAvgEntry = snap.EntryPrice
			totalQty = snap.Qty.Abs()
		}
		h.mu.RUnlock()
		h.emitEvent(natsapi.HelmEvent{
			Code:            CodePositionAdded,
			Symbol:          symbol,
			Side:            side,
			Qty:             totalQty,           // total position qty after add
			Price:           cumulativeAvgPrice, // this add's fill price
			PositionID:      posID,
			EntryPrice:      newAvgEntry, // new blended avg across all legs
			Reason:          source,
			Msg:             "position: pyramid add",
			AvailableCash:   availCash,
			DeployedCapital: deployed,
			Equity:          equity,
		})
	case preFillPhase == position.PhaseEntering:
		// New position confirmed open.
		h.emitEvent(natsapi.HelmEvent{
			Code:            CodePositionOpened,
			Symbol:          symbol,
			Side:            side,
			Qty:             cumulativeQty,
			Price:           cumulativeAvgPrice,
			PositionID:      posID,
			EntryPrice:      cumulativeAvgPrice,
			Reason:          source,
			Msg:             "position: opened",
			AvailableCash:   availCash,
			DeployedCapital: deployed,
			Equity:          equity,
		})
	}

	// ── 7. Poslog GC — purge when flat ────────────────────────────────────────
	// Once all legs are closed/orphaned, the poslog for this hand carries only
	// stale terminal events that serve no purpose on restart (ReplayHand would
	// return them but the state machine would immediately mark the hand as flat
	// anyway). Purging now keeps the HELM_POSITIONS stream lean and makes the
	// next ReplayHand instant (empty slice → reconciler skips restorePosition).
	//
	// Run in a goroutine: PurgeStream is a single NATS API call (~1ms) but we
	// don't want to block the fill hot path on any transient latency.
	// Non-fatal on error: the stream will be purged on the next close-to-flat.
	if isClosingFill {
		h.mu.RLock()
		flat := h.pos.IsFlat()
		h.mu.RUnlock()
		if flat && h.helmRuntime.PosLog != nil {
			helmID := h.helmID.String()
			handID := h.id.String()
			go func() {
				defer safe.Recover()
				if err := h.helmRuntime.PosLog.PurgeHand(context.Background(), helmID, handID); err != nil {
					slog.Warn("poslog: purge after flat failed (non-fatal)",
						"hand_id", handID, "helm_id", helmID, "err", err)
				} else {
					slog.Info("poslog: purged after hand reached flat", "hand_id", handID)
				}
			}()
		}
	}

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

	// Reference capital for pct thresholds: realized equity (allocatedCap + closedPnL −
	// commission) so the guard tightens naturally as the hand loses money, rather than
	// referencing a fixed allocation that becomes stale after repeated losses.
	// Falls back to total portfolio equity when realizedEquity is zero (no allocation set).
	ref := h.realizedEquity()
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

// scheduleBracketPlacement launches the exchange OCO bracket placement goroutine.
// Shared between the entry-fill path and the post-restart recovery path.
// bracketQty is the full position size to protect. posID is the poslog position ID
// (used to write KindBracketPlaced on success; empty string skips poslog write).
func (h *Hand) scheduleBracketPlacement(lv exitLevel, symbol string, bracketQty decimal.Decimal, posID string) {
	placer, ok := h.helmRuntime.Exchange.(exchange.ExitOrderPlacer)
	if !ok {
		return
	}
	exitSide := exchange.Sell
	if lv.Side == "sell" { // short → buy to close
		exitSide = exchange.Buy
	}
	market := exchange.MarketSpot
	if h.helmRuntime.Creds.AccountType == exchange.AccountFuturesUSDM ||
		h.helmRuntime.Creds.AccountType == exchange.AccountFuturesCOINM {
		market = exchange.MarketFutures
	}
	h.mu.RLock()
	handCtx := h.ctx
	h.mu.RUnlock()

	go func(el exitLevel) {
		defer safe.Recover()
		bktCtx := handCtx
		var marginMode string
		if h.cfg.futuresConfig != nil {
			marginMode = string(h.cfg.futuresConfig.MarginType)
		}
		// Use actual leg qty at execution time (after publishOrderFilled has set it),
		// not the scheduled bracketQty which may be the ordered qty when partial fills
		// resulted in a smaller actual position (e.g. OKX Unified Account buy capped
		// by available margin, or quote_qty order filled below expected base qty).
		// Fall back to bracketQty if the leg is already gone (position closed by a
		// concurrent exit before the bracket goroutine ran).
		actualBracketQty := bracketQty
		h.mu.RLock()
		if pl := h.pos.PrimaryLeg(); pl != nil && pl.Symbol == symbol && pl.Qty.IsPositive() {
			actualBracketQty = pl.Qty
		}
		h.mu.RUnlock()
		exitReq := exchange.ExitOrderRequest{
			Symbol:     symbol,
			Market:     market,
			Side:       exitSide,
			Qty:        truncateQty(h.helmRuntime.filtersFor(bktCtx, symbol), actualBracketQty),
			StopLoss:   el.StopLoss,
			TakeProfit: el.TakeProfit,
			MarginMode: marginMode,
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
		if cur, ok := h.exitLevels[symbol]; ok {
			cur.ExchangeOrderIDs = result.OrderIDs
			cur.PlacedAt = time.Now()
			h.exitLevels[symbol] = cur
		}
		h.mu.Unlock()
		if posID != "" {
			h.publishBracketPlaced(handCtx, posID, symbol, result.OrderIDs)
		}
	}(lv)
}

// restoreExitLevelsFromPoslog rebuilds exitLevels from the durable poslog-backed
// position state (h.pos) after a restart.
//
// exitLevels is an ephemeral in-memory map — it is populated at runtime by
// resolvePendingExit / scheduleBracketPlacement and is NOT persisted. After a
// restart it starts empty even though the poslog has all the information:
//
//	KindSLUpdated     → leg.StopLoss / TakeProfit
//	KindBracketPlaced → leg.ExchangeOrderIDs
//
// Without this reconstruction:
//   - RecoverBrackets iterates exitLevels and finds nothing → bracket recovery skipped
//   - checkPositionDesync sees hasExit=false → leg treated as UNPROTECTED → EXT_CLOSE
func (h *Hand) restoreExitLevelsFromPoslog() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, leg := range h.pos.ActiveLegs() {
		if leg.Phase != position.PhaseOpen {
			slog.Debug("startup: restoreExitLevels — leg not PhaseOpen, skip",
				"hand_id", h.id, "symbol", leg.Symbol, "phase", leg.Phase)
			continue
		}
		if !leg.StopLoss.IsPositive() && !leg.TakeProfit.IsPositive() && len(leg.ExchangeOrderIDs) == 0 {
			slog.Info("startup: restoreExitLevels — leg has no exit management (no SL/TP, no OCO ids)",
				"hand_id", h.id, "symbol", leg.Symbol,
				"qty", leg.Qty, "entry_price", leg.EntryPrice)
			continue
		}
		lv := exitLevel{
			Side:             leg.Side,
			StopLoss:         leg.StopLoss,
			TakeProfit:       leg.TakeProfit,
			ExchangeOrderIDs: append([]string(nil), leg.ExchangeOrderIDs...),
		}
		h.exitLevels[leg.Symbol] = lv
		slog.Info("startup: restoreExitLevels — exit level restored from poslog",
			"hand_id", h.id,
			"symbol", leg.Symbol,
			"qty", leg.Qty,
			"sl", lv.StopLoss,
			"tp", lv.TakeProfit,
			"exchange_order_ids", lv.ExchangeOrderIDs,
			"has_oco", len(lv.ExchangeOrderIDs) > 0,
		)
	}
}

// RecoverBrackets reconciles exchange-side OCO bracket orders after a restart.
//
// Three scenarios handled:
//  1. ExchangeOrderIDs empty (crash before KindBracketPlaced) → re-place bracket.
//  2. ExchangeOrderIDs present, bracket already triggered during downtime ("filled") →
//     synthesize a bracket fill now so the position closes cleanly before Sync() runs.
//     Without this, Sync() detects a portfolio desync and emits EXT_CLOSE with an
//     approximate price instead of recording the real OCO execution price.
//  3. ExchangeOrderIDs present, bracket cancelled externally during downtime →
//     route to HandleExitOrderCanceled (disown + orphan detection).
//
// Safe to call on a running hand — uses the actor's mutex and launches goroutines.
// Must be called after RecoverGapFills so fills from downtime are already applied
// (prevents re-placing a bracket for a position that is now flat).
func (h *Hand) RecoverBrackets(ctx context.Context) {
	h.mu.RLock()
	type toPlaceEntry struct {
		symbol string
		lv     exitLevel
		posID  string
		qty    decimal.Decimal
	}
	type toCheckEntry struct {
		symbol string
		id     string
	}
	var toPlace []toPlaceEntry
	var toCheck []toCheckEntry

	slog.Info("startup: RecoverBrackets — scanning exitLevels",
		"hand_id", h.id, "exit_levels_count", len(h.exitLevels))

	for symbol, lv := range h.exitLevels {
		if len(lv.ExchangeOrderIDs) > 0 {
			slog.Info("startup: RecoverBrackets — checking known bracket IDs",
				"hand_id", h.id, "symbol", symbol,
				"sl", lv.StopLoss, "tp", lv.TakeProfit,
				"order_ids", lv.ExchangeOrderIDs)
			for _, id := range lv.ExchangeOrderIDs {
				toCheck = append(toCheck, toCheckEntry{symbol: symbol, id: id})
			}
			continue
		}
		if !lv.StopLoss.IsPositive() && !lv.TakeProfit.IsPositive() {
			continue
		}
		slog.Info("startup: RecoverBrackets — no OCO ids, will re-place bracket",
			"hand_id", h.id, "symbol", symbol,
			"sl", lv.StopLoss, "tp", lv.TakeProfit)
		if pl := h.pos.PrimaryLeg(); pl != nil && pl.Symbol == symbol {
			toPlace = append(toPlace, toPlaceEntry{
				symbol: symbol, lv: lv,
				posID: pl.PositionID, qty: pl.Qty.Abs(),
			})
		}
	}
	h.mu.RUnlock()

	// ── Scenario 2 & 3: check brackets with known IDs ────────────────────────
	for _, c := range toCheck {
		result, err := h.helmRuntime.Exchange.GetOrder(ctx, h.helmRuntime.Creds, c.id)
		if err != nil {
			slog.Warn("startup: RecoverBrackets — GetOrder failed",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id, "err", err)
			continue
		}
		slog.Info("startup: RecoverBrackets — bracket status from exchange",
			"hand_id", h.id, "symbol", c.symbol, "order_id", c.id,
			"status", result.Status,
			"filled_qty", result.FilledQty,
			"filled_avg", result.FilledAvg,
		)
		switch result.Status {
		case "filled":
			// Bracket was triggered and filled during downtime.
			// Route through EnqueueFill (actor loop) rather than calling applyFill
			// directly — RecoverBrackets runs while the hand's actor loop is already
			// active (called after StartAllHydrated), so direct applyFill is a race.
			h.mu.Lock()
			if _, alreadySeen := h.seenFills[c.id]; alreadySeen {
				h.mu.Unlock()
				continue
			}
			h.seenFills[c.id] = time.Now()
			h.mu.Unlock()

			closeSide := string(result.Side)
			if closeSide == "" {
				closeSide = "sell"
			}
			slog.Info("hand: bracket filled during downtime — enqueuing recovery fill",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id,
				"filled_qty", result.FilledQty, "filled_avg", result.FilledAvg)
			if result.FilledQty.IsPositive() && result.FilledAvg.IsPositive() {
				h.EnqueueFill(exchange.WsFillEvent{
					OrderID:   c.id,
					TradeID:   c.id + "_recovery",
					Symbol:    c.symbol,
					Side:      exchange.OrderSide(closeSide),
					FilledQty: result.FilledQty,
					FilledAvg: result.FilledAvg,
				})
			}

		case "canceled", "cancelled", "expired", "rejected":
			// Bracket was externally cancelled during downtime.
			slog.Info("hand: bracket cancelled during downtime — handling external close",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id, "status", result.Status)
			h.HandleExitOrderCanceled(ctx, c.id)

		default:
			// "new" / "submitted" — bracket still live; no action needed.
			slog.Debug("hand: bracket still live after restart",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id, "status", result.Status)
		}
	}

	// ── Scenario 1: re-place brackets where IDs were never recorded ──────────
	// Before placing a new bracket, query the exchange for existing live algo orders
	// for each symbol. If a matching OCO already exists (placed before the crash but
	// before KindBracketPlaced was written), adopt its IDs instead of creating a
	// duplicate — two concurrent OCOs for the same position would cause "insufficient
	// balance" errors when the second tries to sell a position already closed by the first.
	algoLister, hasAlgoLister := h.helmRuntime.Exchange.(exchange.AlgoOrderLister)

	for _, p := range toPlace {
		if p.qty.IsZero() {
			continue
		}

		// Check for existing live algo orders matching this symbol/SL/TP.
		if hasAlgoLister {
			existing, err := algoLister.ListLiveAlgoOrders(ctx, h.helmRuntime.Creds, p.symbol)
			if err != nil {
				slog.Warn("hand: RecoverBrackets — ListLiveAlgoOrders failed (will re-place)",
					"hand_id", h.id, "symbol", p.symbol, "err", err)
			} else {
				var adopted string
				for _, ao := range existing {
					// Match by SL/TP proximity (within 1 tick) — exact match unreliable
					// due to rounding in the original PlaceExitOrders call.
					slMatch := p.lv.StopLoss.IsZero() || p.lv.StopLoss.Sub(ao.StopLoss).Abs().LessThanOrEqual(decimal.NewFromFloat(0.01))
					tpMatch := p.lv.TakeProfit.IsZero() || p.lv.TakeProfit.Sub(ao.TakeProfit).Abs().LessThanOrEqual(decimal.NewFromFloat(0.01))
					if slMatch && tpMatch {
						adopted = ao.AlgoID
						break
					}
				}
				if adopted != "" {
					slog.Info("hand: found existing live algo — adopting IDs instead of re-placing",
						"hand_id", h.id, "symbol", p.symbol, "algo_id", adopted)
					h.trackOrder(adopted)
					h.mu.Lock()
					if lv, ok := h.exitLevels[p.symbol]; ok {
						lv.ExchangeOrderIDs = []string{adopted}
						h.exitLevels[p.symbol] = lv
					}
					h.mu.Unlock()
					h.publishBracketPlaced(ctx, p.posID, p.symbol, []string{adopted})
					continue
				}
			}
		}

		slog.Info("hand: re-placing bracket after restart — KindBracketPlaced was not confirmed",
			"hand_id", h.id, "symbol", p.symbol,
			"stop_loss", p.lv.StopLoss, "take_profit", p.lv.TakeProfit, "qty", p.qty)
		h.scheduleBracketPlacement(p.lv, p.symbol, p.qty, p.posID)
	}
}

// resolvePendingExit resolves raw SL/TP from a just-filled entry order into an absolute
// exitLevel, updates h.exitLevels[symbol], and returns whether a bracket should be placed.
//
// MUST be called with h.mu held (for exitLevels read/write and cancelExitOrders).
//
// price/qty are the fill price and fill qty.
// preQty/preAvg are the leg's qty/avg BEFORE this fill (zero for first entry).
//
// Returns (resolved exitLevel, hasExitBracket, offsetResolved).
func (h *Hand) resolvePendingExit(
	ctx context.Context,
	symbol string, pending exitPending,
	price, qty, preQty, preAvg decimal.Decimal,
) (resolvedEl exitLevel, hasExitBracket bool, offsetResolved bool) {
	resolved := exitLevel{Side: pending.Side}
	if pending.IsOffset {
		// ── avg-anchor (pyramiding-design.md) ──
		// Anchor offsets to the blended avg entry AFTER this add so the SL/TP tracks
		// the merged cost. First entry: preQty=0 → anchor == fill price.
		anchor := price
		if preQty.IsPositive() {
			if newQty := preQty.Add(qty); newQty.IsPositive() {
				anchor = preQty.Mul(preAvg).Add(qty.Mul(price)).Div(newQty)
			}
		}
		if anchor.IsPositive() {
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
		prec := priceTick(h.helmRuntime.filtersFor(ctx, symbol).PriceTick)
		if pending.StopLoss.IsPositive() {
			resolved.StopLoss = pending.StopLoss.Round(prec)
		}
		if pending.TakeProfit.IsPositive() {
			resolved.TakeProfit = pending.TakeProfit.Round(prec)
		}
		// Persist absolute SL/TP to poslog so they survive restart.
		// offsetResolved is true only for offset mode — for absolute levels we
		// must emit KindSLUpdated here so restoreExitLevelsFromPoslog can rebuild
		// exitLevels with correct levels on restart (needed for bracket re-placement
		// in the crash-window scenario where KindBracketPlaced was not written).
		if resolved.StopLoss.IsPositive() || resolved.TakeProfit.IsPositive() {
			offsetResolved = true // reuse the flag to trigger KindSLUpdated in applyFill
		}
	}

	// Pyramid add: cancel prior consolidated bracket before overwriting exitLevels.
	if old, ok := h.exitLevels[symbol]; ok && len(old.ExchangeOrderIDs) > 0 {
		h.cancelExitOrders(ctx, symbol, "")
	}
	h.exitLevels[symbol] = resolved

	if resolved.StopLoss.IsPositive() || resolved.TakeProfit.IsPositive() {
		slTpOK := true
		if resolved.StopLoss.IsPositive() && resolved.TakeProfit.IsPositive() {
			if resolved.Side == "buy" { // long → SL < TP
				slTpOK = resolved.StopLoss.LessThan(resolved.TakeProfit)
			} else { // short → TP < SL
				slTpOK = resolved.TakeProfit.LessThan(resolved.StopLoss)
			}
			if !slTpOK {
				slog.Warn("fill: SL/TP relationship invalid after rounding — skipping OCO bracket",
					"hand_id", h.id, "symbol", symbol, "side", resolved.Side,
					"stop_loss", resolved.StopLoss, "take_profit", resolved.TakeProfit)
			}
		}
		if slTpOK {
			resolvedEl = resolved
			hasExitBracket = true
		}
	}
	return
}

// completeWsFillFromREST is called in the REST-immediate path when the WS fill event
// arrived before PlaceOrder returned (WS beats REST race).
//
// The WS path ran applyFill which:
//
//	✅ Called ReportFill (portfolio correctly updated)
//	❌ publishOrderFilled returned early — pendingOrderPos[orderID] was "" at WS time
//	   → leg stuck in PhaseEntering, never transitions to PhaseOpen
//	   → h.pos.ActiveCount()>0 blocks new entries (MAXUNITS)
//	   → EXIT signal sees no PhaseOpen leg → "NO_POS"
//	❌ pendingExits not consumed (not set at WS time) → no bracket
//
// Now that PlaceOrder has returned, pendingOrderPos and pendingExits are both populated.
// This method completes the fill processing without re-running ReportFill:
//  1. publishOrderFilled → leg PhaseEntering → PhaseOpen (poslog + h.pos)
//  2. Resolve pendingExits → schedule bracket
//  3. Emit KindSLUpdated if offset levels were resolved (needed for poslog replay)
//
// pending is the exitPending captured from line 678 of applyPlaceResult (already in scope).
// restQty is the net fill qty after base-asset commission adjustment.
func (h *Hand) completeWsFillFromREST(
	ctx context.Context,
	orderID, symbol, side string,
	restQty, fillAvg, commission decimal.Decimal,
	pending exitPending,
	isExitOrder bool,
) {
	slog.Info("hand: WS beat REST — completing poslog fill and bracket placement",
		"hand_id", h.id, "order_id", orderID, "symbol", symbol,
		"qty", restQty, "price", fillAvg)

	// Capture posID BEFORE publishOrderFilled deletes it from pendingOrderPos.
	h.mu.RLock()
	posIDForBracket := h.pendingOrderPos[orderID]
	h.mu.RUnlock()

	// 1. publishOrderFilled: transitions leg from PhaseEntering → PhaseOpen
	//    (entry) or PhaseExiting → PhaseIdle (exit).
	//    Does NOT call ReportFill (portfolio already updated by WS applyFill).
	deployedCapital := restQty.Mul(fillAvg).Add(commission)
	if isExitOrder {
		deployedCapital = decimal.Zero // no new capital deployed on close
	}
	h.publishOrderFilled(ctx, orderID, restQty, restQty, fillAvg,
		decimal.Zero, commission, deployedCapital, "rest", "")

	// 1b. For exit orders: cancel the OCO bracket and clear exitLevels.
	// The WS applyFill path could not detect isClosingFill (pendingOrderPos was
	// empty at WS arrival time), so cancelExitOrders was never called. Without
	// this, bracket orders remain live at the exchange after the position closes,
	// and checkExits auto-stops the hand on the next tick.
	if isExitOrder {
		h.mu.Lock()
		h.cancelExitOrders(ctx, symbol, orderID)
		delete(h.exitLevels, symbol)
		h.mu.Unlock()
		return
	}

	// 2. Resolve pendingExits and schedule OCO bracket (entry fills only).
	if !pending.StopLoss.IsPositive() && !pending.TakeProfit.IsPositive() && !pending.IsOffset {
		return
	}
	h.mu.Lock()
	delete(h.pendingExits, orderID) // consume (was set at line ~680 of applyPlaceResult)
	resolvedEl, hasExitBracket, offsetResolved := h.resolvePendingExit(
		ctx, symbol, pending, fillAvg, restQty, decimal.Zero, decimal.Zero,
	)
	h.mu.Unlock()

	if hasExitBracket {
		h.scheduleBracketPlacement(resolvedEl, symbol, restQty, posIDForBracket)
	}

	// 3. Persist resolved offset SL/TP to poslog so restart replay sees the absolute levels
	//    (same KindSLUpdated emitted by the normal applyFill path at step 4).
	if offsetResolved && posIDForBracket != "" &&
		(resolvedEl.StopLoss.IsPositive() || resolvedEl.TakeProfit.IsPositive()) {
		var newSL, newTP string
		if resolvedEl.StopLoss.IsPositive() {
			newSL = resolvedEl.StopLoss.String()
		}
		if resolvedEl.TakeProfit.IsPositive() {
			newTP = resolvedEl.TakeProfit.String()
		}
		payload, _ := json.Marshal(poslog.SLUpdatedPayload{
			OrderID: posIDForBracket,
			NewSL:   newSL,
			NewTP:   newTP,
			Reason:  "offset_resolved",
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:         posIDForBracket + "_sl_resolved_" + orderID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: posIDForBracket,
			Kind:       poslog.KindSLUpdated,
			Payload:    payload,
			At:         time.Now().UTC(),
		})
	}
}
