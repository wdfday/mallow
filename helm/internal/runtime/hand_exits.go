package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/position"
	"mallow/helm/internal/safe"
)

// cancelExitOrders cancels any exchange-side SL/TP orders for the given symbol.
// Must be called while h.mu is held (reads exitLevels without locking).
// Launches a goroutine to avoid blocking the caller on network I/O, detached from
// h.ctx on its own bounded timeout instead — Release/Kill call Stop() right after
// triggering this cancel, and tying the goroutine to h.ctx raced Stop()'s cancel()
// against the in-flight HTTP call ("context canceled"), sometimes leaving the
// SL/TP/algo bracket un-cancelled on the exchange. The 10s timeout below already
// bounds the goroutine's lifetime, so it doesn't need h.ctx to avoid leaking.
func (h *Hand) cancelExitOrders(_ context.Context, symbol string, skipOrderID string) {
	lv, ok := h.exitLevels[symbol]
	if !ok || len(lv.ExchangeOrderIDs) == 0 {
		return
	}
	ids := make([]string, 0, len(lv.ExchangeOrderIDs))
	for _, id := range lv.ExchangeOrderIDs {
		if id != skipOrderID {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	// Mark as helm-initiated cancels BEFORE launching goroutine.
	// When OrderEventCanceled arrives for these IDs, the handler checks
	// pendingCancels to distinguish helm-side cleanup from external closes.
	// Caller holds h.mu so this write is safe.
	for _, id := range ids {
		h.pendingCancels[id] = struct{}{}
	}
	go func() {
		defer safe.Recover()
		cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, id := range ids {
			if err := h.helmRuntime.Exchange.CancelOrder(cancelCtx, h.helmRuntime.Creds, id); err != nil {
				h.log.Warn("hand: cancel exit order failed", "symbol", symbol, "order_id", id, "err", err)
			} else {
				h.log.Info("hand: exit order cancelled", "symbol", symbol, "order_id", id)
			}
		}
	}()
}

// flattenPositions closes this hand's own open legs with market orders. Called by Kill.
// Only closes the qty tracked in this hand's poslog — does not touch other hands' positions.
// Returns the order IDs of flatten orders that did NOT fill synchronously (the REST ack
// came back before the exchange confirmed execution) — Kill waits on these via
// awaitFlattenFills before tearing down the run loop, so the real fill (arriving shortly
// after via WS/poll) has a chance to land instead of being silently lost.
func (h *Hand) flattenPositions(ctx context.Context) []string {
	var pendingOrderIDs []string
	for _, leg := range h.pos.ActiveLegs() {
		if leg.Phase == position.PhaseEntering {
			h.log.Info("hand: kill flattening pending entry order", "symbol", leg.Symbol, "order_id", leg.PendingOrderID)
			if err := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, leg.PendingOrderID); err != nil {
				h.log.Error("hand: kill cancel pending entry order failed", "symbol", leg.Symbol, "order_id", leg.PendingOrderID, "err", err)
			}

			payload, _ := json.Marshal(poslog.OrderCancelledPayload{
				OrderID: leg.PendingOrderID,
				Reason:  "kill",
			})
			h.publishAndApply(ctx, poslog.Event{
				ID:         leg.PendingOrderID + "_cancelled",
				HandID:     h.id.String(),
				HelmID:     h.helmID.String(),
				PositionID: leg.PositionID,
				Kind:       poslog.KindOrderCancelled,
				Payload:    payload,
				At:         time.Now().UTC(),
			})
			continue
		}

		if leg.Phase == position.PhaseAdding {
			h.log.Info("hand: kill flattening pending add order", "symbol", leg.Symbol, "order_id", leg.PendingOrderID)
			if err := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, leg.PendingOrderID); err != nil {
				h.log.Error("hand: kill cancel pending add order failed", "symbol", leg.Symbol, "order_id", leg.PendingOrderID, "err", err)
			}

			payload, _ := json.Marshal(poslog.OrderCancelledPayload{
				OrderID: leg.PendingOrderID,
				Reason:  "kill",
			})
			h.publishAndApply(ctx, poslog.Event{
				ID:         leg.PendingOrderID + "_cancelled",
				HandID:     h.id.String(),
				HelmID:     h.helmID.String(),
				PositionID: leg.PositionID,
				Kind:       poslog.KindOrderCancelled,
				Payload:    payload,
				At:         time.Now().UTC(),
			})
		}

		// Cancel exchange-side bracket orders (OCO) for this symbol synchronously before placing the market close order.
		h.mu.Lock()
		lv, ok := h.exitLevels[leg.Symbol]
		var exitOrderIDs []string
		if ok && len(lv.ExchangeOrderIDs) > 0 {
			exitOrderIDs = make([]string, len(lv.ExchangeOrderIDs))
			copy(exitOrderIDs, lv.ExchangeOrderIDs)
			for _, id := range exitOrderIDs {
				h.pendingCancels[id] = struct{}{}
			}
		}
		h.mu.Unlock()

		for _, id := range exitOrderIDs {
			h.log.Info("hand: kill flattening cancelling exit order", "symbol", leg.Symbol, "order_id", id)
			if err := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, id); err != nil {
				h.log.Error("hand: kill cancel exit order failed", "symbol", leg.Symbol, "order_id", id, "err", err)
			}
		}

		closeSide := exchange.Sell
		closeSideStr := "sell"
		if leg.Side == "sell" { // short position → buy to close
			closeSide = exchange.Buy
			closeSideStr = "buy"
		}
		qty := leg.Qty.Abs()
		// Spot exchanges enforce LOT_SIZE step (typically 0.0001 for most pairs).
		// Truncate to 4 decimal places to avoid filter rejection on kill flatten.
		// Futures use ReduceOnly and manage precision differently — skip truncation.
		if !h.cfg.isFutures {
			qty = truncateQty(h.helmRuntime.filtersFor(ctx, leg.Symbol), qty)
		}
		if qty.IsZero() {
			h.log.Info("hand: kill flatten qty rounds to zero — dust exit (no exchange order)",
				"symbol", leg.Symbol, "original_qty", leg.Qty.Abs())
			continue
		}
		result, err := h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, exchange.OrderRequest{
			Symbol: leg.Symbol,
			Side:   closeSide,
			Type:   exchange.Market,
			Qty:    qty,
		})
		if err != nil {
			h.log.Error("hand: kill flatten failed", "symbol", leg.Symbol, "err", err)
			continue
		}
		h.log.Info("hand: kill flatten order placed", "symbol", leg.Symbol,
			"side", closeSide, "qty", qty, "order_id", result.ID)
		h.metrics.ordersPlaced.Add(1)
		h.emitEvent(natsapi.HelmEvent{
			Code:    CodeOrderPlaced,
			Symbol:  leg.Symbol,
			Side:    string(closeSide),
			Qty:     qty,
			OrderID: result.ID,
			Msg:     "hand: kill flatten order placed",
		})

		// Register in poslog: publish KindOrderPlaced(isClose=true) so the leg
		// transitions Open → Exiting and pendingOrderPos is populated.
		// When the fill arrives (WS, poll, or REST-immediate below), applyFill
		// will detect isClosingFill=true and emit position_closed correctly.
		positionID := leg.PositionID
		placedPayload, _ := json.Marshal(poslog.OrderPlacedPayload{
			OrderID:   result.ID,
			Symbol:    leg.Symbol,
			Side:      closeSideStr,
			Qty:       qty.String(),
			Price:     "0",
			OrderType: "market",
			IsClose:   true,
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:         result.ID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: positionID,
			Kind:       poslog.KindOrderPlaced,
			Payload:    placedPayload,
			At:         time.Now().UTC(),
		})

		// Add to h.orders so pollOrders can detect a delayed fill.
		now := time.Now().UTC()
		h.mu.Lock()
		h.orders = append(h.orders, handdomain.Order{
			HandId:     h.id.String(),
			HelmId:     h.helmID.String(),
			ID:         result.ID,
			Symbol:     leg.Symbol,
			Side:       closeSideStr,
			Qty:        qty,
			Type:       "market",
			Status:     result.Status,
			FilledQty:  result.FilledQty,
			FilledAvg:  result.FilledAvg,
			SubmitTime: now,
		})
		h.mu.Unlock()
		h.trackOrder(result.ID)

		// Handle REST-immediate fill (market orders commonly fill synchronously).
		if result.Status == "filled" {
			h.mu.Lock()
			h.seenFills[result.ID] = time.Now()
			h.mu.Unlock()
			h.applyFill(ctx, result.ID, leg.Symbol, closeSideStr, result.FilledQty, result.FilledAvg, decimal.Zero, "kill")
		} else {
			pendingOrderIDs = append(pendingOrderIDs, result.ID)
		}
	}

	h.mu.Lock()
	h.exitLevels = make(map[string]exitLevel)
	h.mu.Unlock()
	return pendingOrderIDs
}

// awaitFlattenFills blocks briefly so any flatten order that didn't fill synchronously
// (REST ack raced the exchange's execution) has a chance to reach this hand through the
// normal WS/poll fill path — which needs the run loop alive — before Kill calls Stop()
// and tears it down. Without this wait, a flatten fill arriving moments after Stop()
// would sit in the run loop's fill queue undrained forever: the position never actually
// closes in the portfolio even though the asset was really sold at the exchange.
//
// Polls h.seenFills (set by handleWsFill/pollOrders/REST-immediate — whichever path
// actually applies the fill) rather than re-querying the exchange, so this only confirms
// what the hand's own bookkeeping already recorded. Bounded — gives up and lets Kill
// proceed to Stop() after the timeout so Kill can never hang indefinitely.
func (h *Hand) awaitFlattenFills(orderIDs []string) {
	if len(orderIDs) == 0 {
		return
	}
	const timeout = 5 * time.Second
	const pollInterval = 100 * time.Millisecond
	deadline := time.Now().Add(timeout)
	for {
		h.mu.RLock()
		var stillPending []string
		for _, id := range orderIDs {
			if _, seen := h.seenFills[id]; !seen {
				stillPending = append(stillPending, id)
			}
		}
		h.mu.RUnlock()
		if len(stillPending) == 0 {
			return
		}
		if time.Now().After(deadline) {
			h.log.Warn("hand: kill flatten fill not confirmed before shutdown", "order_ids", stillPending, "timeout", timeout)
			return
		}
		time.Sleep(pollInterval)
	}
}

// HandleExitOrderCanceled is called when the exchange fires OrderEventCanceled for
// a bracket/OCO order ID that was tracked by this hand.
//
// Two cases:
//  1. Helm-initiated cancel (cancelExitOrders marked the ID in pendingCancels):
//     → normal OCO sibling cleanup; just clear pendingCancels entry and return.
//  2. External cancel (ID not in pendingCancels):
//     → user closed the position manually at the exchange; the bracket order was
//     cancelled by Binance as a side-effect. Emit KindPositionClosed so the leg
//     is cleared from poslog + in-memory state immediately, without waiting for restart.
func (h *Hand) HandleExitOrderCanceled(ctx context.Context, orderID string) {
	h.mu.Lock()
	// Case 1: helm-initiated — normal cleanup.
	if _, helmCancelled := h.pendingCancels[orderID]; helmCancelled {
		delete(h.pendingCancels, orderID)
		h.mu.Unlock()
		return
	}
	// Case 2: external cancel — find the leg that owns this order ID.
	var affectedLeg *position.LegState
	var affectedSymbol string
	for sym, lv := range h.exitLevels {
		for _, id := range lv.ExchangeOrderIDs {
			if id == orderID {
				affectedSymbol = sym
				break
			}
		}
		if affectedSymbol != "" {
			break
		}
	}
	// Also search active legs for the matching symbol.
	if affectedSymbol != "" {
		for _, leg := range h.pos.ActiveLegs() {
			if leg.Symbol == affectedSymbol {
				affectedLeg = leg
				break
			}
		}
	}

	remainingCount := 0
	if affectedSymbol != "" {
		if lv, ok := h.exitLevels[affectedSymbol]; ok {
			filtered := lv.ExchangeOrderIDs[:0]
			for _, id := range lv.ExchangeOrderIDs {
				if id != orderID {
					filtered = append(filtered, id)
				}
			}
			lv.ExchangeOrderIDs = filtered
			h.exitLevels[affectedSymbol] = lv
			remainingCount = len(filtered)
		}
	}
	h.mu.Unlock()

	// Case 3: OCO counterpart auto-cancel — drop the cancelled ID and let the
	// triggered leg's fill close the position normally.
	//
	// Covers two sub-cases:
	//   a) PhaseExiting: helm placed a signal exit order; Binance auto-cancels the
	//      resting bracket partner (SL cancelled when TP fills, or vice versa).
	//   b) PhaseOpen: a bracket leg (SL or TP) triggered and the exchange auto-cancelled
	//      its OCO partner. This happens when helm reconnects after downtime and the WS
	//      delivers the cancel event BEFORE the fill event for the triggered leg.
	//      In both sub-cases the fill is still in-flight via WS, or will be caught by
	//      the bracket poller's next GetOrder on the remaining IDs.
	//
	// We only return early here if we are currently exiting (PhaseExiting) or if the leg
	// is PhaseOpen but there is still at least one active order remaining in the bracket.
	// If remainingCount == 0 for a PhaseOpen leg, it means BOTH bracket orders have been
	// cancelled externally without any fill, so we must proceed to disown/orphan.
	if affectedLeg != nil && (affectedLeg.Phase == position.PhaseExiting || (affectedLeg.Phase == position.PhaseOpen && remainingCount > 0)) {
		h.log.Info("hand: OCO partner cancelled — counterpart auto-cancel (partner triggered or fill in-flight)",
			"symbol", affectedSymbol, "order_id", orderID, "phase", affectedLeg.Phase, "remaining", remainingCount)
		return
	}

	if affectedLeg == nil {
		// Not a bracket order — check if it is a pending entry or pyramid add order
		// that was cancelled externally (e.g. user cancels from exchange UI, GTX/IOC
		// rejection). The poll path (applyPolledOrders) also handles this, but WS may
		// arrive first. We apply KindOrderCancelled idempotently so whichever path wins,
		// the second sees an empty pendingOrderPos and skips.
		h.mu.RLock()
		entryPosID := h.pendingOrderPos[orderID]
		preCancelPhase := h.pos.LegPhase(entryPosID)
		h.mu.RUnlock()

		if entryPosID == "" ||
			(preCancelPhase != position.PhaseEntering && preCancelPhase != position.PhaseAdding) {
			return
		}

		h.log.Info("hand: entry/add order cancelled via WS",
			"order_id", orderID, "phase", preCancelPhase)

		payload, _ := json.Marshal(poslog.OrderCancelledPayload{
			OrderID: orderID,
			Reason:  "ws_cancel",
		})
		// Use the same deterministic ID as the poll path so JetStream dedup
		// prevents a double poslog event if both WS and poll paths fire.
		h.publishAndApply(ctx, poslog.Event{
			ID:         orderID + "_cancelled",
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: entryPosID,
			Kind:       poslog.KindOrderCancelled,
			Payload:    payload,
			At:         time.Now().UTC(),
		})

		switch preCancelPhase {
		case position.PhaseEntering:
			h.emitEvent(natsapi.HelmEvent{
				Code:    CodePositionEnterCancelled,
				OrderID: orderID,
				Reason:  "ws_cancel",
				Msg:     "position: entry order cancelled externally — no position opened",
			})
		case position.PhaseAdding:
			h.emitEvent(natsapi.HelmEvent{
				Code:    CodePositionAddCancelled,
				OrderID: orderID,
				Reason:  "ws_cancel",
				Msg:     "position: add order cancelled externally — position reverts to prior state",
			})
		}
		return
	}

	h.log.Warn("hand: external position close detected via bracket order cancel",
		"symbol", affectedSymbol,
		"order_id", orderID,
		"position_id", affectedLeg.PositionID,
	)
	h.emitEvent(natsapi.HelmEvent{
		Code:   CodePositionExtClosed,
		Symbol: affectedSymbol,
		Reason: fmt.Sprintf("bracket order %s cancelled by exchange (not helm-initiated)", orderID),
		Msg:    "hand: position externally closed — user manual exit detected",
	})

	now := time.Now().UTC()
	// Cancelling the bracket revokes the hand's mandate over this leg's capital — by
	// contract the hand DISOWNS the leg rather than claiming a close. The position is
	// left at the exchange (now the user's), so we emit KindPositionOrphaned, NOT
	// KindPositionClosed: no realized PnL is booked. An audit trade record with
	// gross_pnl=0 and exit_reason="orphaned" is written so the event is visible in
	// trade history but excluded from KPI aggregates (win rate, Sharpe).
	//
	// SCOPE: this disown contract carries SPOT semantics. On spot, a resting OCO sell locks
	// the base balance, so cancelling it is a strong "user is taking over" signal. Futures
	// brackets are reduce-only and do NOT lock margin, so a cancel there is a weak signal
	// (likely a stop adjustment, not a takeover) — when futures support lands, this path must
	// re-evaluate: re-place the bracket (restore protection) instead of disowning. Futures is
	// out of scope today, so the simple unconditional disown is left as-is.
	payload, _ := json.Marshal(poslog.PositionOrphanedPayload{
		Symbol: affectedLeg.Symbol,
		Source: "manual",
	})
	// Deterministic dedup ID — safe to replay on restart; the reconciler never reclaims an orphaned leg.
	h.publishAndApply(ctx, poslog.Event{
		ID:         h.id.String() + "_extcancel_" + affectedLeg.PositionID,
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: affectedLeg.PositionID,
		Kind:       poslog.KindPositionOrphaned,
		Payload:    payload,
		At:         now,
	})
	// Write audit trade record: exit_price="", gross_pnl="0", exit_reason="orphaned".
	h.appendOrphanTradeRecord(ctx, affectedLeg, "manual")

	// Clear local exit level tracking for this symbol.
	h.mu.Lock()
	delete(h.exitLevels, affectedSymbol)
	h.mu.Unlock()
}

// rearmLocalExit hands a position's protection back to the in-process SL/TP monitor
// after its exchange OCO was cancelled (signal-exit pre-flight) but the exit could not
// be placed. checkExits skips the local trigger while ExchangeOrderIDs is non-empty —
// but those IDs now point at cancelled orders that will never fire, so the position is
// effectively naked. Clearing the IDs (keeping the SL/TP levels) lets checkExits resume
// triggering on price crosses. The leg stays open; the strategy may still re-emit an exit.
func (h *Hand) rearmLocalExit(symbol string) {
	h.mu.Lock()
	el, ok := h.exitLevels[symbol]
	if !ok || (el.StopLoss.IsZero() && el.TakeProfit.IsZero()) {
		h.mu.Unlock()
		h.log.Error("hand: exit failed with no local SL/TP to fall back on — position UNPROTECTED",
			"symbol", symbol)
		return
	}
	el.ExchangeOrderIDs = nil // dead (cancelled) — stop checkExits from skipping
	h.exitLevels[symbol] = el
	h.mu.Unlock()
	h.log.Warn("hand: exit failed — exchange OCO cancelled, local SL/TP monitor re-armed",
		"symbol", symbol, "stop_loss", el.StopLoss, "take_profit", el.TakeProfit)
	h.emitEvent(natsapi.HelmEvent{
		Code:   CodeOrderExitFailed,
		Symbol: symbol,
		Reason: "exit order failed; exchange OCO cancelled — local SL/TP monitor re-armed",
		Msg:    "hand: exit failed — local SL/TP now guarding the open position",
	})
}

// orphanLegsForSymbol disowns every active leg of a symbol WITHOUT booking a trade:
// it emits KindPositionOrphaned (durable; the reconciler never reclaims an orphaned
// leg) and drops the leg from the shared portfolio — no ReportFill, no PnL, no
// round-trip trade. Used when an exit cannot cleanly complete (e.g. the wallet's
// free base balance is short of this hand's share), where a partial sell would
// corrupt the trade log. Runs ON the actor loop (single-owner) — callers must not
// hold tradeMu (RemovePosition acquires it).
func (h *Hand) orphanLegsForSymbol(ctx context.Context, symbol, source string) {
	for _, leg := range h.pos.ActiveLegs() {
		if leg.Symbol != symbol {
			continue
		}
		h.emitEvent(natsapi.HelmEvent{
			Code:   CodePositionExtClosed,
			Symbol: symbol,
			Reason: "could not cleanly exit — leg orphaned (" + source + ")",
			Msg:    "hand: position orphaned — audit trade record written (pnl=0)",
		})
		payload, _ := json.Marshal(poslog.PositionOrphanedPayload{Symbol: symbol, Source: source})
		h.publishAndApply(ctx, poslog.Event{
			ID:         h.id.String() + "_orphan_" + source + "_" + leg.PositionID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: leg.PositionID,
			Kind:       poslog.KindPositionOrphaned,
			Payload:    payload,
			At:         time.Now().UTC(),
		})
		// Write audit trade record: exit_price="", gross_pnl="0", exit_reason="orphaned".
		h.appendOrphanTradeRecord(ctx, leg, source)
	}
	h.mu.Lock()
	delete(h.exitLevels, symbol)
	h.mu.Unlock()
}

// releasePositions performs a synthetic close (buyback) at market price for every open leg.
// The position is closed in the poslog and recorded in metrics/trade records, while the
// physical position remains open at the exchange. On restart, the reconciler will not reclaim these legs.
func (h *Hand) releasePositions(ctx context.Context) {
	for _, leg := range h.pos.ActiveLegs() {
		closeSide := "sell"
		if leg.Side == "sell" { // short position → buy to close
			closeSide = "buy"
		}
		qty := leg.Qty.Abs()
		price := h.helmRuntime.lastKnownPrice(leg.Symbol)

		releaseID := fmt.Sprintf("release_%s_%d", leg.Symbol, time.Now().UnixNano())

		h.mu.Lock()
		h.pendingOrderPos[releaseID] = leg.PositionID
		h.mu.Unlock()

		// Transition leg to PhaseExiting so applyFill detects isClosingFill.
		placedPayload, _ := json.Marshal(poslog.OrderPlacedPayload{
			OrderID:   releaseID,
			Symbol:    leg.Symbol,
			Side:      closeSide,
			Qty:       qty.String(),
			Price:     "0",
			OrderType: "market",
			IsClose:   true,
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:         releaseID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: leg.PositionID,
			Kind:       poslog.KindOrderPlaced,
			Payload:    placedPayload,
			At:         time.Now().UTC(),
		})

		// Cancel exchange-side bracket orders BEFORE the synthetic fill.
		// applyFill may clear exitLevels[sym] on close detection; cancelling first
		// ensures the order IDs are still readable from exitLevels when cancelExitOrders runs.
		h.mu.Lock()
		h.cancelExitOrders(ctx, leg.Symbol, "")
		h.mu.Unlock()

		// Apply synthetic fill → emits KindPositionClosed + trade record.
		h.applyFill(ctx, releaseID, leg.Symbol, closeSide, qty, price, decimal.Zero, "release")
	}
	h.mu.Lock()
	h.exitLevels = make(map[string]exitLevel)
	h.mu.Unlock()
}

// checkExits scans open positions against locally stored SL/TP levels.
// Called on every pollTicker tick (every 30s) as a safety net in case
// exchange-side bracket orders fail to execute.
//
// exitLevels[sym] is deleted unconditionally right after DeliverSignal, without checking
// whether the send succeeded or was dropped — DeliverSignal returns void, so there is
// nothing to check. This is safe ONLY because DeliverSignal is now the sole writer to
// h.Signals and serialises all callers under signalsMu (2026-07-06): drain-then-send
// under that lock is guaranteed to succeed (the only other party touching the channel is
// the single consumer in runLoop, which just makes room, never adds a competing value) —
// so the drop branch inside DeliverSignal is effectively unreachable from this call site.
// If a second writer to h.Signals is ever added outside DeliverSignal, this guarantee
// breaks and the delete here would need to become conditional on delivery succeeding.
func (h *Hand) checkExits() {
	h.mu.RLock()
	exits := make(map[string]exitLevel, len(h.exitLevels))
	for sym, el := range h.exitLevels {
		exits[sym] = el
	}
	h.mu.RUnlock()

	for sym, el := range exits {
		price := h.helmRuntime.lastKnownPrice(sym)
		if !price.IsPositive() {
			continue
		}
		var reason string
		if el.Side == "buy" { // long position
			if el.StopLoss.IsPositive() && price.LessThanOrEqual(el.StopLoss) {
				reason = "stop_loss"
			} else if el.TakeProfit.IsPositive() && price.GreaterThanOrEqual(el.TakeProfit) {
				reason = "take_profit"
			}
		} else { // short position
			if el.StopLoss.IsPositive() && price.GreaterThanOrEqual(el.StopLoss) {
				reason = "stop_loss"
			} else if el.TakeProfit.IsPositive() && price.LessThanOrEqual(el.TakeProfit) {
				reason = "take_profit"
			}
		}
		if reason == "" {
			continue
		}

		// Guard against external close desync: if THIS hand no longer holds any
		// active position but exitLevels still has an entry, the position was closed
		// outside the hand (manual trade or another hand).
		// Use h.pos.ActiveCount() (per-hand, poslog-backed), NOT Portfolio.GetPosition
		// which aggregates ALL hands on the helm — a co-located hand holding the same
		// symbol would suppress this guard and cause spurious closes.
		h.mu.RLock()
		handActive := h.pos.ActiveCount() > 0
		h.mu.RUnlock()
		if !handActive {
			h.mu.Lock()
			delete(h.exitLevels, sym)
			h.mu.Unlock()
			h.log.Warn("exit monitor: hand flat but exitLevels present — external close detected, stopping hand",
				"symbol", sym)
			h.emitEvent(natsapi.HelmEvent{
				Code:   CodeHandAutoStopped,
				Symbol: sym,
				Reason: "external close detected: hand flat but local exit level present",
				Msg:    "hand: auto-stopped due to position desync",
			})
			go h.Stop()
			continue
		}

		// Exchange-side bracket orders are active — they will close the position.
		// Skip the local trigger to avoid double-closing (exchange SL fills first,
		// then this fires and places a redundant sell against a flat position).
		if len(el.ExchangeOrderIDs) > 0 {
			h.log.Debug("exit monitor: skipping local trigger — exchange-side orders active",
				"symbol", sym, "exchange_orders", el.ExchangeOrderIDs)
			continue
		}

		h.log.Info("exit monitor triggered", "symbol", sym,
			"reason", reason, "price", price,
			"stop_loss", el.StopLoss, "take_profit", el.TakeProfit)

		h.emitEvent(natsapi.HelmEvent{
			Code:      CodeOrderExitTriggered,
			Symbol:    sym,
			Direction: string(strategy.DirExit),
			Price:     price,
			Reason:    fmt.Sprintf("exit monitor %s triggered (SL: %s, TP: %s)", reason, el.StopLoss, el.TakeProfit),
			Msg:       "order: local exit trigger activated",
		})

		// Deliver into Signals so the exit is processed on the next run-loop tick.
		// Goes through DeliverSignal (not a duplicated drain-replace) so this shares the
		// same lock + drop-tracking as NATS-delivered signals — see DeliverSignal doc.
		h.DeliverSignal(Signal{Symbol: sym, Direction: strategy.DirExit, Strength: 1.0, ReceivedAt: time.Now()})
		h.mu.Lock()
		delete(h.exitLevels, sym)
		h.mu.Unlock()
	}
}

// checkBracketOrders polls the exchange status of active OCO/bracket order IDs
// stored in exitLevels. Called every pollTicker tick (5s) alongside checkExits.
//
// If a bracket order is no longer active (cancelled/filled) at the exchange and
// helm did not initiate the cancel (not in pendingCancels), the position was
// externally closed — user manual exit or exchange-side close. This covers the
// gap where the WS OrderEventCanceled is missed (restart, brief disconnect, etc.).
// bracketState pairs a bracket exit order with its freshly-fetched exchange status.
// result is nil when err != nil. Produced by fetchBracketStates, consumed by applyBracketStates.
type bracketState struct {
	symbol string
	id     string
	result *exchange.OrderResult
	err    error
}

// fetchBracketStates is the I/O phase: GetOrder for all live bracket legs per symbol.
// Pure REST over a snapshot of exitLevels — no hand-state mutation — so it runs OFF the
// actor loop alongside fetchPendingOrders. applyBracketStates handles the result on-loop.
//
// All non-pending IDs are checked (not just the first) so that exchanges with two-legged
// OCO brackets (e.g. Binance: separate TP + SL order IDs) can surface the filled leg even
// when the cancelled leg is polled first.
// bracketPollGrace is the minimum age a bracket must be before checkBracketOrders
// will poll it via REST. Newly placed orders are not yet visible in OKX's active
// or history endpoints — polling them immediately returns not_found, which the
// poller would otherwise treat as an external close (→ EXT_CLOSE).
const bracketPollGrace = 30 * time.Second

func (h *Hand) fetchBracketStates(ctx context.Context) []bracketState {
	h.mu.RLock()
	var checks []bracketState
	now := time.Now()
	for sym, lv := range h.exitLevels {
		if len(lv.ExchangeOrderIDs) == 0 {
			continue
		}
		// Skip symbols whose bracket was placed too recently — the exchange may not
		// have propagated the order yet, so GetOrder/getAlgoOrder would return
		// not_found and falsely trigger pollExternalClose.
		if !lv.PlacedAt.IsZero() && now.Sub(lv.PlacedAt) < bracketPollGrace {
			continue
		}
		for _, id := range lv.ExchangeOrderIDs {
			if _, pending := h.pendingCancels[id]; !pending {
				checks = append(checks, bracketState{symbol: sym, id: id})
			}
		}
	}
	h.mu.RUnlock()

	for i := range checks {
		checks[i].result, checks[i].err = h.helmRuntime.Exchange.GetOrder(ctx, h.helmRuntime.Creds, checks[i].id)
	}
	return checks
}

// pollExternalClose is called by applyBracketStates when the exchange confirms an order is
// terminally gone (canceled/not_found/filled-no-data) via explicit REST poll. Unlike the WS
// path, the poller is authoritative — we remove the ID from pendingCancels first so that
// HandleExitOrderCanceled always takes the external-close path, even if helm raced a cancel
// attempt (cancel-before-replace) that added this ID to pendingCancels.
func (h *Hand) pollExternalClose(ctx context.Context, orderID string) {
	h.mu.Lock()
	delete(h.pendingCancels, orderID)
	h.mu.Unlock()
	h.HandleExitOrderCanceled(ctx, orderID)
}

// applyBracketStates is the state-mutation phase: runs ON the actor loop. A bracket gone
// canceled/expired externally means the position likely closed → HandleExitOrderCanceled.
func (h *Hand) applyBracketStates(ctx context.Context, checks []bracketState) {
	// When multiple IDs were checked for the same symbol (e.g. Binance OCO: TP + SL as
	// separate orders), pick the best result per symbol using a priority ranking:
	//   filled  >  active (new/pending/partial)  >  terminal (cancelled/not_found/error)
	//
	// Without the active > terminal tier, a Binance OCO poll that returns
	// [SL: "not_found", TP: "new"] in that order would select SL (first-wins) and
	// falsely trigger pollExternalClose, writing KindPositionOrphaned to the poslog
	// even though the position is still live at the exchange.
	bracketIsActive := func(c bracketState) bool {
		if c.err != nil || c.result == nil {
			return false
		}
		switch c.result.Status {
		case "new", "partially_filled", "pending_new", "submitted", "accepted", "pending":
			return true
		}
		return false
	}
	best := make(map[string]bracketState, len(checks))
	for _, c := range checks {
		prev, ok := best[c.symbol]
		if !ok {
			best[c.symbol] = c
			continue
		}
		prevFilled := prev.err == nil && prev.result != nil && prev.result.Status == "filled"
		curFilled := c.err == nil && c.result != nil && c.result.Status == "filled"
		switch {
		case curFilled && !prevFilled:
			best[c.symbol] = c
		case !prevFilled && !curFilled && bracketIsActive(c) && !bracketIsActive(prev):
			// Upgrade from a terminal/error result to a known-active order — the position
			// is still open, so the active result is authoritative.
			best[c.symbol] = c
		}
	}

	for _, c := range best {
		if c.err != nil {
			h.log.Warn("checkBracketOrders: GetOrder failed (transient?)",
				"symbol", c.symbol, "order_id", c.id, "err", c.err)
			continue
		}
		switch c.result.Status {
		case "filled":
			// Bracket triggered and filled — WS fill event was missed (reconnect, brief
			// disconnect, or slow delivery). Synthesise the fill now so the position closes
			// with the real OCO execution price rather than an EXT_CLOSE at last-known price.
			// Guard with seenFills to avoid double-apply if WS eventually delivers too.
			h.mu.Lock()
			if _, alreadySeen := h.seenFills[c.id]; alreadySeen {
				h.mu.Unlock()
				break
			}
			h.seenFills[c.id] = time.Now()
			h.mu.Unlock()

			if c.result.FilledQty.IsPositive() && c.result.FilledAvg.IsPositive() {
				closeSide := string(c.result.Side)
				if closeSide == "" {
					closeSide = "sell"
				}
				legLabel := "bracket"
				if c.result.Tag == "tp" {
					legLabel = "TP"
				} else if c.result.Tag == "sl" {
					legLabel = "SL"
				}
				h.log.Info("checkBracketOrders: bracket filled (WS missed) — applying poll fill",
					"symbol", c.symbol, "order_id", c.id,
					"leg", legLabel, "qty", c.result.FilledQty, "avg", c.result.FilledAvg)
				h.applyFill(ctx, c.id, c.symbol, closeSide,
					c.result.FilledQty, c.result.FilledAvg, decimal.Zero, "bracket_poll")
			} else {
				// OCO triggered but exchange returned no fill price/qty (e.g. OKX actualSz/actualPx
				// empty). Poller confirmed order is terminal — bypass pendingCancels guard and treat
				// as external close so the leg doesn't linger even if helm raced a cancel attempt.
				h.log.Warn("checkBracketOrders: bracket filled but no fill data — external close",
					"symbol", c.symbol, "order_id", c.id)
				h.pollExternalClose(ctx, c.id)
			}

		case "canceled", "cancelled", "expired", "rejected":
			// Poller confirmed the order is gone with no fill. Regardless of whether helm
			// placed this ID in pendingCancels (cancel-before-replace race), the exchange is
			// authoritative: the OCO was cancelled externally — position likely closed by user.
			h.log.Warn("checkBracketOrders: bracket order cancelled externally — position likely closed",
				"symbol", c.symbol, "order_id", c.id, "status", c.result.Status)
			h.pollExternalClose(ctx, c.id)

		case "not_found":
			// Algo order has vanished from both the active and history endpoints — triggered,
			// expired, or purged without a WS delivery. Bypass pendingCancels — poller is authoritative.
			h.log.Warn("checkBracketOrders: bracket order not found at exchange — external close",
				"symbol", c.symbol, "order_id", c.id)
			h.pollExternalClose(ctx, c.id)
		}
	}
}

// handlePositionDesync closes the leg locally due to external desync.
// Called by HelmRuntime during the sync desync check.
func (h *Hand) handlePositionDesync(ctx context.Context, leg *position.LegState) {
	legQty := leg.Qty.Abs()
	h.log.Warn("checkPositionDesync: portfolio qty < leg qty — external close suspected",
		"symbol", leg.Symbol, "position_id", leg.PositionID,
		"leg_qty", legQty,
	)
	h.emitEvent(natsapi.HelmEvent{
		Code:   CodePositionExtClosed,
		Symbol: leg.Symbol,
		Reason: fmt.Sprintf("position externally closed — detected via portfolio desync"),
		Msg:    "hand: position externally closed — detected via portfolio desync",
	})

	now := time.Now().UTC()
	// Orphan, not closed: a desync (portfolio qty < leg qty, no bracket/signal exit of
	// ours in flight) means something moved this position outside helm's control — the
	// same "we don't actually know what happened" situation as the external-close branch
	// in HandleExitOrderCanceled and orphanLegsForSymbol. Those two correctly emit
	// KindPositionOrphaned with no claimed PnL; this one used to emit KindPositionClosed
	// with a PnL guessed from lastKnownPrice (never a real fill price) and feed it into
	// appendTradeRecord — a fabricated number counted into win-rate/Sharpe like a genuine
	// trade. Same underlying event (CodePositionExtClosed), inconsistent treatment. Fixed
	// 2026-07-10 to match the other two: KindPositionOrphaned + appendOrphanTradeRecord.
	payload, _ := json.Marshal(poslog.PositionOrphanedPayload{Symbol: leg.Symbol, Source: "desync"})
	h.publishAndApply(ctx, poslog.Event{
		ID:         h.id.String() + "_desync_" + leg.PositionID,
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: leg.PositionID,
		Kind:       poslog.KindPositionOrphaned,
		Payload:    payload,
		At:         now,
	})
	// Portfolio already updated by Sync() — no need to call RemovePosition.
	// If partial close, portfolio still has exchangeQty; removing is wrong.
	// Let the next Sync() or WS fill settle the remainder.
	h.appendOrphanTradeRecord(ctx, leg, "desync")

	h.mu.Lock()
	delete(h.exitLevels, leg.Symbol)
	h.mu.Unlock()
}

// closeLegAsDust closes the poslog position for symbol without placing an exchange
// order. Used when an exit order is rejected because the qty is below the exchange's
// minimum lot size — the unsold dust remains in the helm-level portfolio.
//
// Sequence:
//  1. Registers a synthetic order ID in pendingOrderPos so applyFill can resolve the leg.
//  2. Emits KindOrderPlaced(IsClose=true) → transitions leg PhaseOpen→PhaseExiting.
//  3. Calls applyFill("dust_exit") → detects isClosingFill=true → emits KindPositionClosed.
func (h *Hand) closeLegAsDust(ctx context.Context, symbol, side string, qty, price decimal.Decimal) {
	h.mu.RLock()
	var posID string
	for _, leg := range h.pos.ActiveLegs() {
		if leg.Symbol == symbol && (leg.Phase == position.PhaseOpen || leg.Phase == position.PhaseAdding) {
			posID = leg.PositionID
			break
		}
	}
	h.mu.RUnlock()
	if posID == "" {
		return // already flat — nothing to close
	}

	dustID := fmt.Sprintf("dust_%s_%d", symbol, time.Now().UnixNano())

	h.mu.Lock()
	h.pendingOrderPos[dustID] = posID
	// Snapshot bracket IDs before clearing exitLevels.
	// Mark them in seenFills + remove routing so that if the exchange-side bracket
	// fill event (e.g. orders-algo WS for OKX) arrives AFTER this synthetic close,
	// it is recognised as already handled and doesn't trigger a second ReportFill
	// (which would double-sell the portfolio, producing a negative SOL balance).
	var bracketIDs []string
	if lv, ok := h.exitLevels[symbol]; ok {
		bracketIDs = append(bracketIDs, lv.ExchangeOrderIDs...)
	}
	for _, id := range bracketIDs {
		h.seenFills[id] = time.Now()
	}
	// Clear the exit level so checkExits stops retrying.
	delete(h.exitLevels, symbol)
	h.mu.Unlock()

	// Remove routing for bracket IDs off the actor loop — prevents the orphan
	// path from double-applying their fills after this synthetic close.
	for _, id := range bracketIDs {
		h.helmRuntime.RemoveOrderTracking(id)
	}

	// Transition leg to PhaseExiting so applyFill detects isClosingFill.
	placedPayload, _ := json.Marshal(poslog.OrderPlacedPayload{
		OrderID:   dustID,
		Symbol:    symbol,
		Side:      side,
		Qty:       qty.String(),
		Price:     "0",
		OrderType: "market",
		IsClose:   true,
	})
	h.publishAndApply(ctx, poslog.Event{
		ID:         dustID,
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: posID,
		Kind:       poslog.KindOrderPlaced,
		Payload:    placedPayload,
		At:         time.Now().UTC(),
	})

	// Apply synthetic fill → emits KindPositionClosed + trade record.
	// price=0 when last known price unavailable; PnL will be reported as zero.
	h.applyFill(ctx, dustID, symbol, side, qty, price, decimal.Zero, "dust_exit")
}
