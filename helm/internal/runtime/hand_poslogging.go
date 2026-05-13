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
func (b *Hand) publishAndApply(ctx context.Context, e poslog.Event) {
	if b.rt.PosLog != nil {
		if err := b.rt.PosLog.Publish(ctx, e); err != nil {
			slog.Error("poslog publish failed", "hand_id", b.id, "event_id", e.ID, "kind", e.Kind, "err", err)
		}
	}
	b.mu.Lock()
	if err := b.pos.Apply(e); err != nil {
		slog.Warn("poslog in-memory apply failed", "hand_id", b.id, "event_id", e.ID, "err", err)
	}
	// Keep pendingOrderPos in sync.
	switch e.Kind {
	case poslog.KindOrderPlaced:
		var p poslog.OrderPlacedPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			b.pendingOrderPos[p.OrderID] = e.PositionID
		}
	case poslog.KindOrderFilled:
		var p poslog.OrderFilledPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			delete(b.pendingOrderPos, p.OrderID)
		}
	case poslog.KindOrderCancelled:
		var p poslog.OrderCancelledPayload
		if jsonErr := unmarshalJSON(e.Payload, &p); jsonErr == nil {
			delete(b.pendingOrderPos, p.OrderID)
		}
	}
	b.mu.Unlock()
}

// publishOrderPlaced emits KindOrderPlaced to the durable poslog.
// isExitIntent: true when closing a position (not opening or adding).
func (b *Hand) publishOrderPlaced(
	ctx context.Context,
	orderID, symbol string,
	reply helmdomain.TradeReply,
	limitPrice decimal.Decimal,
	orderType exchange.OrderType,
	isExitIntent bool,
) {
	// Determine pyramid add vs new leg vs close.
	b.mu.RLock()
	isFlat := b.pos.IsFlat()
	primaryPosID := ""
	if leg := b.pos.PrimaryLeg(); leg != nil {
		primaryPosID = leg.PositionID
	}
	b.mu.RUnlock()

	isClose := isExitIntent
	isPyramidAdd := !isClose && b.pyramid && !isFlat

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
	})
	b.publishAndApply(ctx, poslog.Event{
		ID:         orderID,
		HandID:     b.id.String(),
		HelmID:     b.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderPlaced,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}

// publishOrderFilled emits KindOrderFilled to the durable poslog.
func (b *Hand) publishOrderFilled(ctx context.Context, orderID string, qty, price decimal.Decimal, source string) {
	b.mu.RLock()
	positionID := b.pendingOrderPos[orderID]
	isClosingFill := positionID != "" && b.pos.LegPhase(positionID) == position.PhaseExiting
	b.mu.RUnlock()

	if positionID == "" {
		return // order not tracked in poslog (e.g., PlaceOrder manual method without poslog)
	}

	payload, _ := json.Marshal(poslog.OrderFilledPayload{
		OrderID:   orderID,
		FillPrice: price.String(),
		FillQty:   qty.String(),
		Source:    source,
	})
	b.publishAndApply(ctx, poslog.Event{
		ID:         orderID + "_filled",
		HandID:     b.id.String(),
		HelmID:     b.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderFilled,
		Payload:    payload,
		At:         time.Now().UTC(),
	})

	// For close fills, also emit position_closed with the computed PnL.
	if isClosingFill {
		closedPayload, _ := json.Marshal(poslog.PositionClosedPayload{
			OrderID:     positionID,
			ClosePrice:  price.String(),
			RealizedPnL: "0", // approximate — authoritative value is in HandPositions.RealizedPnL
			Source:      "signal",
		})
		b.publishAndApply(ctx, poslog.Event{
			ID:         positionID + "_closed_" + orderID,
			HandID:     b.id.String(),
			HelmID:     b.helmID.String(),
			PositionID: positionID,
			Kind:       poslog.KindPositionClosed,
			Payload:    closedPayload,
			At:         time.Now().UTC(),
		})
	}
}
