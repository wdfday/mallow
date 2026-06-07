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
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/position"
	"mallow/helm/internal/safe"
)

// cancelExitOrders cancels any exchange-side SL/TP orders for the given symbol.
// Must be called while h.mu is held (reads exitLevels without locking).
// Launches a goroutine to avoid blocking the caller on network I/O.
// Uses the hand's lifecycle context so the goroutine exits immediately when
// Hand.Stop() is called — prevents goroutine leaks during shutdown.
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
	// Capture hand context under existing lock (caller already holds h.mu).
	handCtx := h.ctx
	go func() {
		defer safe.Recover()
		cancelCtx, cancel := context.WithTimeout(handCtx, 10*time.Second)
		defer cancel()
		for _, id := range ids {
			if err := h.helmRuntime.Exchange.CancelOrder(cancelCtx, h.helmRuntime.Creds, id); err != nil {
				slog.Warn("hand: cancel exit order failed", "hand_id", h.id, "symbol", symbol, "order_id", id, "err", err)
			} else {
				slog.Info("hand: exit order cancelled", "hand_id", h.id, "symbol", symbol, "order_id", id)
			}
		}
	}()
}

// flattenPositions closes this hand's own open legs with market orders. Called by Kill.
// Only closes the qty tracked in this hand's poslog — does not touch other hands' positions.
func (h *Hand) flattenPositions(ctx context.Context) {
	for _, leg := range h.pos.ActiveLegs() {
		if leg.Phase == position.PhaseEntering {
			slog.Info("hand: kill flattening pending entry order", "hand_id", h.id, "symbol", leg.Symbol, "order_id", leg.PendingOrderID)
			if err := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, leg.PendingOrderID); err != nil {
				slog.Error("hand: kill cancel pending entry order failed", "hand_id", h.id, "symbol", leg.Symbol, "order_id", leg.PendingOrderID, "err", err)
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
			slog.Info("hand: kill flattening pending add order", "hand_id", h.id, "symbol", leg.Symbol, "order_id", leg.PendingOrderID)
			if err := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, leg.PendingOrderID); err != nil {
				slog.Error("hand: kill cancel pending add order failed", "hand_id", h.id, "symbol", leg.Symbol, "order_id", leg.PendingOrderID, "err", err)
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
		isFutures := h.helmRuntime.Creds.AccountType == exchange.AccountFuturesUSDM ||
			h.helmRuntime.Creds.AccountType == exchange.AccountFuturesCOINM
		if !isFutures {
			qty = truncateQty(h.helmRuntime.filtersFor(ctx, leg.Symbol), qty)
		}
		if qty.IsZero() {
			slog.Info("hand: kill flatten qty rounds to zero — dust exit (no exchange order)",
				"hand_id", h.id, "symbol", leg.Symbol, "original_qty", leg.Qty.Abs())
			continue
		}
		result, err := h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, exchange.OrderRequest{
			Symbol: leg.Symbol,
			Side:   closeSide,
			Type:   exchange.Market,
			Qty:    qty,
		})
		if err != nil {
			slog.Error("hand: kill flatten failed", "hand_id", h.id, "symbol", leg.Symbol, "err", err)
			continue
		}
		slog.Info("hand: kill flatten order placed", "hand_id", h.id, "symbol", leg.Symbol,
			"side", closeSide, "qty", qty, "order_id", result.ID)
		h.metrics.ordersPlaced.Add(1)

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
		}
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
	h.mu.Unlock()

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

		slog.Info("hand: entry/add order cancelled via WS",
			"hand_id", h.id, "order_id", orderID, "phase", preCancelPhase)

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

	slog.Warn("hand: external position close detected via bracket order cancel",
		"hand_id", h.id,
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
	// KindPositionClosed: no realized PnL is booked and no round-trip trade is recorded
	// (the outcome belongs to the user and is unknown). See ACTOR_MODEL.md / hand_exits.md.
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

	// Drop the leg from the hand's view of the shared portfolio. We do NOT call ReportFill
	// (no exit price / no PnL) and do NOT append a trade record — this is a disown, not a
	// trade. If the position is still live at the exchange, the next Sync() re-surfaces it
	// as an unattributed helm-level position.
	// RemovePosition acquires tradeMu internally — Hand must never lock tradeMu directly to
	// avoid a mutex-ordering violation (tradeMu is owned by HelmRuntime, not Hand).
	h.helmRuntime.RemovePosition(affectedSymbol)

	// Clear local exit level tracking for this symbol.
	h.mu.Lock()
	delete(h.exitLevels, affectedSymbol)
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
// Called on every pollTicker tick (every 5s) as a safety net in case
// exchange-side bracket orders fail to execute.
//
// IMPORTANT: exitLevels[sym] is deleted ONLY after the DirExit signal is
// successfully enqueued into UrgentSignals. If the channel is full the level
// is preserved so the next tick can retry — prevents the safety net from
// being silently lost when the urgent buffer is momentarily saturated.
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
			slog.Warn("exit monitor: hand flat but exitLevels present — external close detected, stopping hand",
				"hand_id", h.id, "symbol", sym)
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
			slog.Debug("exit monitor: skipping local trigger — exchange-side orders active",
				"hand_id", h.id, "symbol", sym, "exchange_orders", el.ExchangeOrderIDs)
			continue
		}

		slog.Info("exit monitor triggered", "hand_id", h.id, "symbol", sym,
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

		// Delete exitLevels only after signal is successfully enqueued.
		// If UrgentSignals is full, we keep the level intact and retry next tick.
		select {
		case h.UrgentSignals <- Signal{Symbol: sym, Direction: strategy.DirExit, Strength: 1.0, ReceivedAt: time.Now()}:
			h.mu.Lock()
			delete(h.exitLevels, sym)
			h.mu.Unlock()
		default:
			slog.Warn("exit monitor: urgent channel full, will retry next tick", "hand_id", h.id, "symbol", sym)
		}
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
func (h *Hand) fetchBracketStates(ctx context.Context) []bracketState {
	h.mu.RLock()
	var checks []bracketState
	for sym, lv := range h.exitLevels {
		if len(lv.ExchangeOrderIDs) == 0 {
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
// terminally gone (cancelled/not_found/filled-no-data) via explicit REST poll. Unlike the WS
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
	// separate orders), pick the best result per symbol: prefer "filled" so we close with
	// the real execution price instead of falling through to the external-close path.
	best := make(map[string]bracketState, len(checks))
	for _, c := range checks {
		prev, ok := best[c.symbol]
		if !ok {
			best[c.symbol] = c
			continue
		}
		// Upgrade if current is filled and previous is not.
		prevFilled := prev.err == nil && prev.result != nil && prev.result.Status == "filled"
		curFilled := c.err == nil && c.result != nil && c.result.Status == "filled"
		if curFilled && !prevFilled {
			best[c.symbol] = c
		}
	}

	for _, c := range best {
		if c.err != nil {
			slog.Warn("checkBracketOrders: GetOrder failed (transient?)",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id, "err", c.err)
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
				slog.Info("checkBracketOrders: bracket filled (WS missed) — applying poll fill",
					"hand_id", h.id, "symbol", c.symbol, "order_id", c.id,
					"qty", c.result.FilledQty, "avg", c.result.FilledAvg)
				h.applyFill(ctx, c.id, c.symbol, closeSide,
					c.result.FilledQty, c.result.FilledAvg, decimal.Zero, "bracket_poll")
			} else {
				// OCO triggered but exchange returned no fill price/qty (e.g. OKX actualSz/actualPx
				// empty). Poller confirmed order is terminal — bypass pendingCancels guard and treat
				// as external close so the leg doesn't linger even if helm raced a cancel attempt.
				slog.Warn("checkBracketOrders: bracket filled but no fill data — external close",
					"hand_id", h.id, "symbol", c.symbol, "order_id", c.id)
				h.pollExternalClose(ctx, c.id)
			}

		case "canceled", "cancelled", "expired", "rejected":
			// Poller confirmed the order is gone with no fill. Regardless of whether helm
			// placed this ID in pendingCancels (cancel-before-replace race), the exchange is
			// authoritative: the OCO was cancelled externally — position likely closed by user.
			slog.Warn("checkBracketOrders: bracket order cancelled externally — position likely closed",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id, "status", c.result.Status)
			h.pollExternalClose(ctx, c.id)

		case "not_found":
			// Algo order has vanished from both the active and history endpoints — triggered,
			// expired, or purged without a WS delivery. Bypass pendingCancels — poller is authoritative.
			slog.Warn("checkBracketOrders: bracket order not found at exchange — external close",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id)
			h.pollExternalClose(ctx, c.id)
		}
	}
}

// handlePositionDesync closes the leg locally due to external desync.
// Called by HelmRuntime during the sync desync check.
func (h *Hand) handlePositionDesync(ctx context.Context, leg *position.LegState) {
	legQty := leg.Qty.Abs()
	slog.Warn("checkPositionDesync: portfolio qty < leg qty — external close suspected",
		"hand_id", h.id, "symbol", leg.Symbol, "position_id", leg.PositionID,
		"leg_qty", legQty,
	)
	h.emitEvent(natsapi.HelmEvent{
		Code:   CodePositionExtClosed,
		Symbol: leg.Symbol,
		Reason: fmt.Sprintf("position externally closed — detected via portfolio desync"),
		Msg:    "hand: position externally closed — detected via portfolio desync",
	})

	now := time.Now().UTC()
	closePrice := h.helmRuntime.lastKnownPrice(leg.Symbol)
	var realizedPnL decimal.Decimal
	if closePrice.IsPositive() && leg.EntryPrice.IsPositive() {
		diff := closePrice.Sub(leg.EntryPrice)
		if leg.Side == "sell" { // short: profit when price falls
			diff = diff.Neg()
		}
		realizedPnL = diff.Mul(leg.Qty.Abs())
	}
	cp := poslog.PositionClosedPayload{
		OrderID:     leg.PositionID,
		Symbol:      leg.Symbol,
		Side:        leg.Side,
		Qty:         leg.Qty.Abs().String(),
		EntryPrice:  leg.EntryPrice.String(),
		EntryAt:     leg.OpenedAt,
		ClosePrice:  closePrice.String(), // best-effort; zero when price data unavailable
		RealizedPnL: realizedPnL.String(),
		ExitReason:  "external",
	}
	payload, _ := json.Marshal(cp)
	h.publishAndApply(ctx, poslog.Event{
		ID:         h.id.String() + "_desync_" + leg.PositionID,
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: leg.PositionID,
		Kind:       poslog.KindPositionClosed,
		Payload:    payload,
		At:         now,
	})
	// Portfolio already updated by Sync() — no need to call RemovePosition.
	// If partial close, portfolio still has exchangeQty; removing is wrong.
	// Let the next Sync() or WS fill settle the remainder.
	if closePrice.IsPositive() {
		h.appendTradeRecord(ctx, cp, decimal.Zero, now)
	} else {
		slog.Warn("checkPositionDesync: skipping trade record — close price unknown",
			"hand_id", h.id, "symbol", leg.Symbol, "position_id", leg.PositionID)
	}

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
