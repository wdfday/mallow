package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/poslog"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/position"
)

// publishAndApply publishes a poslog event to JetStream and applies it to the in-memory
// position state. The NATS publish failure is logged but does not halt the hand —
// the reconciler will detect and patch the missing event on next startup.
// Must NOT be called while b.mu is held.
func (h *Hand) publishAndApply(ctx context.Context, e poslog.Event) {
	if h.helmRuntime.PosLog != nil {
		if err := h.helmRuntime.PosLog.Publish(ctx, e); err != nil {
			slog.Error("poslog publish failed", "hand_id", h.id, "event_id", e.ID, "kind", e.Kind, "err", err)
		}
	}
	h.mu.Lock()
	if err := h.pos.Apply(e); err != nil {
		slog.Warn("poslog in-memory apply failed", "hand_id", h.id, "event_id", e.ID, "err", err)
	}
	// Keep pendingOrderPos in sync.
	switch e.Kind {
	case poslog.KindOrderPlaced:
		var p poslog.OrderPlacedPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			h.pendingOrderPos[p.OrderID] = e.PositionID
		}
	case poslog.KindOrderFilled:
		var p poslog.OrderFilledPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			delete(h.pendingOrderPos, p.OrderID)
		}
	case poslog.KindOrderCancelled:
		var p poslog.OrderCancelledPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			delete(h.pendingOrderPos, p.OrderID)
		}
	}
	h.mu.Unlock()
}

// publishOrderPlaced emits KindOrderPlaced to the durable poslog.
// isExitIntent: true when closing a position (not opening or adding).
func (h *Hand) publishOrderPlaced(
	ctx context.Context,
	orderID, symbol string,
	reply helmdomain.TradeReply,
	limitPrice decimal.Decimal,
	orderType exchange.OrderType,
	isExitIntent bool,
	patternKind string,
) {
	// Determine pyramid add vs new leg vs close.
	h.mu.RLock()
	isFlat := h.pos.IsFlat()
	primaryPosID := ""
	if leg := h.pos.PrimaryLeg(); leg != nil {
		primaryPosID = leg.PositionID
	}
	h.mu.RUnlock()

	isClose := isExitIntent
	isPyramidAdd := !isClose && h.pyramid && !isFlat

	var positionID string
	switch {
	case isClose || isPyramidAdd:
		positionID = primaryPosID
	default:
		positionID = orderID // new leg: PositionID = opening order_id
	}
	if positionID == "" {
		return // no active leg context — skip (e.g., close with no open position)
	}

	priceStr := "0"
	if limitPrice.IsPositive() {
		priceStr = limitPrice.String()
	}
	orderTypeStr := "market"
	if orderType == exchange.Limit {
		orderTypeStr = "limit"
	}

	payload, _ := json.Marshal(poslog.OrderPlacedPayload{
		OrderID:      orderID,
		Symbol:       symbol,
		Side:         reply.Side,
		Qty:          reply.Qty.String(),
		Price:        priceStr,
		OrderType:    orderTypeStr,
		StopLoss:     reply.StopLoss.String(),
		TakeProfit:   reply.TakeProfit.String(),
		IsPyramidAdd: isPyramidAdd,
		IsClose:      isClose,
		PatternKind:  patternKind,
	})
	h.publishAndApply(ctx, poslog.Event{
		ID:         orderID,
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderPlaced,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}

// publishOrderFilled emits KindOrderFilled to the durable poslog.
// pnl is the realized PnL for closing fills (zero for entry fills).
// closeSource identifies what triggered the close (e.g. "signal", "sl", "tp", "kill").
func (h *Hand) publishOrderFilled(ctx context.Context, orderID string, qty, price, pnl decimal.Decimal, source, closeSource string) {
	h.mu.RLock()
	positionID := h.pendingOrderPos[orderID]
	var isBracketExit bool
	var snap position.LegSnapshot
	var hasSnap bool
	if positionID == "" {
		// Look for orderID in exitLevels
		for _, lv := range h.exitLevels {
			for _, id := range lv.ExchangeOrderIDs {
				if id == orderID {
					isBracketExit = true
					if primaryLeg := h.pos.PrimaryLeg(); primaryLeg != nil {
						positionID = primaryLeg.PositionID
						snap, hasSnap = h.pos.LegSnapshot(positionID)
					}
					break
				}
			}
			if positionID != "" {
				break
			}
		}
	} else {
		snap, hasSnap = h.pos.LegSnapshot(positionID)
	}
	isClosingFill := isBracketExit || (positionID != "" && h.pos.LegPhase(positionID) == position.PhaseExiting)
	h.mu.RUnlock()

	if positionID == "" {
		return // order not tracked in poslog
	}

	if isBracketExit {
		cp := poslog.PositionClosedPayload{
			OrderID:     positionID,
			ClosePrice:  price.String(),
			RealizedPnL: pnl.String(),
			Source:      closeSource,
		}
		if hasSnap {
			cp.Symbol = snap.Symbol
			cp.Side = snap.Side
			cp.Qty = qty.String() // fill qty, not leg's accumulated qty
			cp.EntryPrice = snap.EntryPrice.String()
			cp.EntryAt = snap.OpenedAt
		}
		closedPayload, _ := json.Marshal(cp)
		h.publishAndApply(ctx, poslog.Event{
			ID:         positionID + "_closed_" + orderID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: positionID,
			Kind:       poslog.KindPositionClosed,
			Payload:    closedPayload,
			At:         time.Now().UTC(),
		})
		return
	}

	payload, _ := json.Marshal(poslog.OrderFilledPayload{
		OrderID:   orderID,
		FillPrice: price.String(),
		FillQty:   qty.String(),
		Source:    source,
	})
	h.publishAndApply(ctx, poslog.Event{
		ID:         orderID + "_filled",
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderFilled,
		Payload:    payload,
		At:         time.Now().UTC(),
	})

	if isClosingFill {
		cp := poslog.PositionClosedPayload{
			OrderID:     positionID,
			ClosePrice:  price.String(),
			RealizedPnL: pnl.String(),
			Source:      closeSource,
		}
		if hasSnap {
			cp.Symbol = snap.Symbol
			cp.Side = snap.Side
			cp.Qty = qty.String() // fill qty, not leg's accumulated qty
			cp.EntryPrice = snap.EntryPrice.String()
			cp.EntryAt = snap.OpenedAt
		}
		closedPayload, _ := json.Marshal(cp)
		h.publishAndApply(ctx, poslog.Event{
			ID:         positionID + "_closed_" + orderID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: positionID,
			Kind:       poslog.KindPositionClosed,
			Payload:    closedPayload,
			At:         time.Now().UTC(),
		})
	}
}
