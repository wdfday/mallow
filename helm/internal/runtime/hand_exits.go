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
			h.seenFills[result.ID] = struct{}{}
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
		// Order ID not associated with any active leg — ignore.
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
	cp := poslog.PositionClosedPayload{
		OrderID:     affectedLeg.PositionID,
		Symbol:      affectedLeg.Symbol,
		Side:        affectedLeg.Side,
		Qty:         affectedLeg.Qty.Abs().String(),
		EntryPrice:  affectedLeg.EntryPrice.String(),
		EntryAt:     affectedLeg.OpenedAt,
		ClosePrice:  decimal.Zero.String(), // unknown — user exited at unknown price
		RealizedPnL: decimal.Zero.String(),
		ExitReason:  "external",
	}
	payload, _ := json.Marshal(cp)

	// Emit KindPositionClosed so the leg is cleared from poslog + in-memory state.
	// Deterministic dedup ID — safe to replay on restart.
	h.publishAndApply(ctx, poslog.Event{
		ID:         h.id.String() + "_extcancel_" + affectedLeg.PositionID,
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: affectedLeg.PositionID,
		Kind:       poslog.KindPositionClosed,
		Payload:    payload,
		At:         now,
	})

	// Remove from shared portfolio. We don't call ReportFill (which adjusts cash)
	// because the exit price is unknown — just drop the position from the aggregate view.
	// Cash accuracy will be restored on the next Sync() from the exchange account.
	h.helmRuntime.Portfolio.RemovePosition(affectedSymbol)

	// Write trade record with price=0 so trade history reflects the event.
	h.appendTradeRecord(ctx, cp, decimal.Zero, now)

	// Clear local exit level tracking for this symbol.
	h.mu.Lock()
	delete(h.exitLevels, affectedSymbol)
	h.mu.Unlock()
}

// releasePositions emits KindPositionOrphaned for every open leg and clears local
// exit-level tracking. The positions remain open at the exchange; no close orders
// are placed. On restart, the reconciler will not reclaim these legs.
func (h *Hand) releasePositions(ctx context.Context) {
	for _, leg := range h.pos.ActiveLegs() {
		payload, _ := json.Marshal(poslog.PositionOrphanedPayload{
			Symbol: leg.Symbol,
			Source: "release",
		})
		h.publishAndApply(ctx, poslog.Event{
			ID:         leg.PositionID + "_orphaned",
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: leg.PositionID,
			Kind:       poslog.KindPositionOrphaned,
			Payload:    payload,
			At:         time.Now().UTC(),
		})
		h.helmRuntime.Portfolio.RemovePosition(leg.Symbol)
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

		// Guard against external close desync: if the portfolio no longer holds
		// this symbol but exitLevels still has an entry, the position was closed
		// outside the hand (manual trade or another hand). Firing a close order
		// against a flat position would open a reverse position on futures margin.
		// Clean up the stale level and pause the hand so a human can review.
		if pos := h.helmRuntime.Portfolio.GetPosition(sym); pos == nil {
			h.mu.Lock()
			delete(h.exitLevels, sym)
			h.mu.Unlock()
			slog.Warn("exit monitor: portfolio flat but exitLevels present — external close detected, pausing hand",
				"hand_id", h.id, "symbol", sym)
			h.emitEvent(natsapi.HelmEvent{
				Code:   CodeHandAutoPaused,
				Symbol: sym,
				Reason: "external close detected: portfolio flat but local exit level present",
				Msg:    "hand: auto-paused due to position desync",
			})
			h.Pause()
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
func (h *Hand) checkBracketOrders(ctx context.Context) {
	h.mu.RLock()
	type check struct {
		symbol string
		id     string
	}
	var checks []check
	for sym, lv := range h.exitLevels {
		if len(lv.ExchangeOrderIDs) == 0 {
			continue
		}
		// Skip IDs already marked as helm-initiated cancels.
		for _, id := range lv.ExchangeOrderIDs {
			if _, pending := h.pendingCancels[id]; !pending {
				checks = append(checks, check{symbol: sym, id: id})
				break // one ID per symbol is enough
			}
		}
	}
	h.mu.RUnlock()

	for _, c := range checks {
		result, err := h.helmRuntime.Exchange.GetOrder(ctx, h.helmRuntime.Creds, c.id)
		if err != nil {
			slog.Debug("checkBracketOrders: GetOrder failed (transient?)",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id, "err", err)
			continue
		}
		switch result.Status {
		case "canceled", "cancelled", "expired", "rejected":
			slog.Info("checkBracketOrders: bracket order cancelled externally — position likely closed",
				"hand_id", h.id, "symbol", c.symbol, "order_id", c.id, "status", result.Status)
			h.HandleExitOrderCanceled(ctx, c.id)
		}
		// "filled" = exchange-side SL/TP triggered; fill event arrives via WS/poll.
	}
}

// checkPositionDesync is a fallback detector for the no-bracket case (no SL/TP placed at
// exchange). It compares each active poslog leg's qty against the shared Portfolio, which is
// authoritative-overwritten by Sync() every SYNC_INTERVAL from the exchange REST API.
//
// If Portfolio.Qty < leg.Qty (exchange holds less than the hand thinks), the position was
// partially or fully closed externally — user manual exit, margin call, exchange liquidation.
// In all such cases the leg is buried: KindPositionClosed is emitted and the leg is cleared.
// Partial-close granularity is intentionally not tracked (too complex, unknown fill price);
// the whole leg is treated as gone.
//
// Limitations:
//   - Detection latency = SYNC_INTERVAL (default 5 min) after Sync() overwrites Portfolio.
//   - Multi-hand same-symbol: Portfolio qty is aggregate across all hands under this helm.
//     A false-positive can fire if another hand's entry fill arrives between Sync ticks.
//     This is the known "multi-hand same-symbol" gap (tracked in BUGS_AND_EDGE_CASES.md).
//   - Legs in PhaseEntering / PhaseAdding / PhaseExiting are skipped to avoid triggering
//     on normal order lifecycle races.
func (h *Hand) checkPositionDesync(ctx context.Context) {
	h.mu.RLock()
	legs := h.pos.ActiveLegs()
	h.mu.RUnlock()

	for _, leg := range legs {
		// Only check fully-open legs — pending orders are fine.
		if leg.Phase != position.PhaseOpen {
			continue
		}
		legQty := leg.Qty.Abs()
		pos := h.helmRuntime.Portfolio.GetPosition(leg.Symbol)
		exchangeQty := decimal.Zero
		if pos != nil {
			exchangeQty = pos.Qty.Abs()
		}
		// Portfolio holds at least as much as the leg → no desync.
		if exchangeQty.GreaterThanOrEqual(legQty) {
			continue
		}
		// Check if the shortfall is just known dust from a truncated exit order.
		// e.g. leg=0.0944055, sold=0.0944, dust=0.0000055 → exchange shows 0.0000055 → skip.
		shortfall := legQty.Sub(exchangeQty)
		if dust := h.helmRuntime.GetDust(leg.Symbol); shortfall.LessThanOrEqual(dust) {
			slog.Debug("checkPositionDesync: shortfall within known dust — skipping",
				"hand_id", h.id, "symbol", leg.Symbol,
				"shortfall", shortfall, "dust", dust,
			)
			continue
		}
		// Exchange holds less than the leg (beyond known dust) → external close detected.
		slog.Warn("checkPositionDesync: portfolio qty < leg qty — external close suspected",
			"hand_id", h.id, "symbol", leg.Symbol, "position_id", leg.PositionID,
			"leg_qty", legQty, "exchange_qty", exchangeQty,
		)
		h.emitEvent(natsapi.HelmEvent{
			Code:   CodePositionExtClosed,
			Symbol: leg.Symbol,
			Reason: fmt.Sprintf("exchange qty %s < leg qty %s after sync — external close suspected", exchangeQty, legQty),
			Msg:    "hand: position externally closed — detected via portfolio desync",
		})

		now := time.Now().UTC()
		cp := poslog.PositionClosedPayload{
			OrderID:     leg.PositionID,
			Symbol:      leg.Symbol,
			Side:        leg.Side,
			Qty:         leg.Qty.Abs().String(),
			EntryPrice:  leg.EntryPrice.String(),
			EntryAt:     leg.OpenedAt,
			ClosePrice:  decimal.Zero.String(), // unknown — synced price not available per-fill
			RealizedPnL: decimal.Zero.String(),
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
		h.appendTradeRecord(ctx, cp, decimal.Zero, now)

		h.mu.Lock()
		delete(h.exitLevels, leg.Symbol)
		h.mu.Unlock()
	}
}
