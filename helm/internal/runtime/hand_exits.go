package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

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
	// Capture hand context under existing lock (caller already holds h.mu).
	handCtx := h.ctx
	go func() {
		cancelCtx, cancel := context.WithTimeout(handCtx, 10*time.Second)
		defer cancel()
		for _, id := range ids {
			if err := h.helmRuntime.Exchange.CancelOrder(cancelCtx, h.helmRuntime.Creds, id); err != nil {
				slog.Warn("bot: cancel exit order failed", "hand_id", h.id, "symbol", symbol, "order_id", id, "err", err)
			} else {
				slog.Info("bot: exit order cancelled", "hand_id", h.id, "symbol", symbol, "order_id", id)
			}
		}
	}()
}

// flattenPositions closes this hand's own open legs with market orders. Called by Kill.
// Only closes the qty tracked in this hand's poslog — does not touch other hands' positions.
func (h *Hand) flattenPositions(ctx context.Context) {
	for _, leg := range h.pos.ActiveLegs() {
		if leg.Phase == position.PhaseEntering {
			slog.Info("bot: kill flattening pending entry order", "hand_id", h.id, "symbol", leg.Symbol, "order_id", leg.PendingOrderID)
			if err := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, leg.PendingOrderID); err != nil {
				slog.Error("bot: kill cancel pending entry order failed", "hand_id", h.id, "symbol", leg.Symbol, "order_id", leg.PendingOrderID, "err", err)
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
			slog.Info("bot: kill flattening pending add order", "hand_id", h.id, "symbol", leg.Symbol, "order_id", leg.PendingOrderID)
			if err := h.helmRuntime.Exchange.CancelOrder(ctx, h.helmRuntime.Creds, leg.PendingOrderID); err != nil {
				slog.Error("bot: kill cancel pending add order failed", "hand_id", h.id, "symbol", leg.Symbol, "order_id", leg.PendingOrderID, "err", err)
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
			slog.Error("bot: kill flatten failed", "hand_id", h.id, "symbol", leg.Symbol, "err", err)
			continue
		}
		slog.Info("bot: kill flatten order placed", "hand_id", h.id, "symbol", leg.Symbol,
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
			h.applyFill(ctx, result.ID, leg.Symbol, closeSideStr, result.FilledQty, result.FilledAvg, "kill")
		}
	}
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
		// outside the bot (manual trade or another hand). Firing a close order
		// against a flat position would open a reverse position on futures margin.
		// Clean up the stale level and pause the hand so a human can review.
		if pos := h.helmRuntime.Portfolio.GetPosition(sym); pos == nil {
			h.mu.Lock()
			delete(h.exitLevels, sym)
			h.mu.Unlock()
			slog.Warn("exit monitor: portfolio flat but exitLevels present — external close detected, pausing hand",
				"hand_id", h.id, "symbol", sym)
			h.helmRuntime.EmitEvent(natsapi.HelmEvent{
				HandID: h.id.String(),
				Code:   CodeHandAutoPaused,
				Symbol: sym,
				Reason: "external close detected: portfolio flat but local exit level present",
				Msg:    "hand: auto-paused due to position desync",
			})
			h.activityLog.push(ActivityEntry{
				At:     time.Now(),
				Code:   CodeHandAutoPaused,
				Symbol: sym,
				Reason: "external close detected: portfolio flat but local exit level present",
			})
			h.Pause()
			continue
		}

		slog.Info("exit monitor triggered", "hand_id", h.id, "symbol", sym,
			"reason", reason, "price", price,
			"stop_loss", el.StopLoss, "take_profit", el.TakeProfit)

		h.helmRuntime.EmitEvent(natsapi.HelmEvent{
			HandID:    h.id.String(),
			Code:      CodeOrderExitTriggered,
			Symbol:    sym,
			Direction: string(strategy.DirExit),
			Price:     price,
			Reason:    fmt.Sprintf("exit monitor %s triggered (SL: %s, TP: %s)", reason, el.StopLoss, el.TakeProfit),
			Msg:       "order: local exit trigger activated",
		})
		h.activityLog.push(ActivityEntry{
			At:        time.Now(),
			Code:      CodeOrderExitTriggered,
			Symbol:    sym,
			Direction: string(strategy.DirExit),
			Reason:    fmt.Sprintf("exit monitor %s triggered at price %s (SL: %s, TP: %s)", reason, price, el.StopLoss, el.TakeProfit),
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
