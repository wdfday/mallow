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
	"mallow/helm/internal/runtime/perf"
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
	// Keep pendingOrderPos in sync with poslog events.
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
	orderID, clientOrderID, symbol string,
	reply helmdomain.TradeReply,
	limitPrice decimal.Decimal,
	orderType exchange.OrderType,
	isExitIntent bool,
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
		OrderID:       orderID,
		ClientOrderID: clientOrderID,
		Symbol:        symbol,
		Side:          reply.Side,
		Qty:           reply.Qty.String(),
		Price:         priceStr,
		OrderType:     orderTypeStr,
		StopLoss:      reply.StopLoss.String(),
		TakeProfit:    reply.TakeProfit.String(),
		IsPyramidAdd:  isPyramidAdd,
		IsClose:       isClose,
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
// commission is the fee paid for this fill (may be zero when exchange doesn't report it).
// closeSource identifies what triggered the close (e.g. "signal", "sl", "tp", "kill").
func (h *Hand) publishOrderFilled(ctx context.Context, orderID string, qty, price, pnl, commission decimal.Decimal, source, closeSource string) {
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
			OrderID:      positionID,
			ClosePrice:   price.String(),
			RealizedPnL:  pnl.String(),
			ExitReason:   closeSource,
			EntryOrderID: positionID,
			ExitOrderID:  orderID,
			Commission:   decimalToString(commission),
		}
		if hasSnap {
			cp.Symbol = snap.Symbol
			cp.Side = snap.Side
			cp.Qty = qty.String() // fill qty, not leg's accumulated qty
			cp.EntryPrice = snap.EntryPrice.String()
			cp.EntryAt = snap.OpenedAt
			if snap.StopLoss.IsPositive() {
				cp.StopLossPrice = snap.StopLoss.String()
			}
			if snap.TakeProfit.IsPositive() {
				cp.TakeProfitPrice = snap.TakeProfit.String()
			}
			cp.NEntries = snap.NEntries
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
		h.appendTradeRecord(ctx, cp, commission, time.Now().UTC())
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
		now := time.Now().UTC()
		cp := poslog.PositionClosedPayload{
			OrderID:      positionID,
			ClosePrice:   price.String(),
			RealizedPnL:  pnl.String(),
			ExitReason:   closeSource,
			EntryOrderID: positionID,
			ExitOrderID:  orderID,
			Commission:   decimalToString(commission),
		}
		if hasSnap {
			cp.Symbol = snap.Symbol
			cp.Side = snap.Side
			cp.Qty = qty.String() // fill qty, not leg's accumulated qty
			cp.EntryPrice = snap.EntryPrice.String()
			cp.EntryAt = snap.OpenedAt
			if snap.StopLoss.IsPositive() {
				cp.StopLossPrice = snap.StopLoss.String()
			}
			if snap.TakeProfit.IsPositive() {
				cp.TakeProfitPrice = snap.TakeProfit.String()
			}
			cp.NEntries = snap.NEntries
		}
		closedPayload, _ := json.Marshal(cp)
		h.publishAndApply(ctx, poslog.Event{
			ID:         positionID + "_closed_" + orderID,
			HandID:     h.id.String(),
			HelmID:     h.helmID.String(),
			PositionID: positionID,
			Kind:       poslog.KindPositionClosed,
			Payload:    closedPayload,
			At:         now,
		})
		h.appendTradeRecord(ctx, cp, commission, now)
	}
}

// appendTradeRecord publishes a completed round-trip trade to the HELM_TRADES
// JetStream stream. A TradePersister drains the stream into PostgreSQL.
//
// JetStream acts as a durable buffer: if the persister or PG is down briefly,
// trades queue up in NATS instead of being lost (which the previous direct-PG
// goroutine path could not guarantee).
//
// No-ops when TradeLog is not configured.
func (h *Hand) appendTradeRecord(ctx context.Context, cp poslog.PositionClosedPayload, commission decimal.Decimal, exitAt time.Time) {
	tl := h.helmRuntime.TradeLog
	if tl == nil {
		return
	}
	rec := perf.TradeRecord{
		HelmID:          h.helmID.String(),
		HandID:          h.id.String(),
		UserID:          h.helmRuntime.UserID.String(),
		Symbol:          cp.Symbol,
		Side:            cp.Side,
		Qty:             cp.Qty,
		Timeframe:       h.Timeframe,
		EntryPrice:      cp.EntryPrice,
		ExitPrice:       cp.ClosePrice,
		EntryAt:         cp.EntryAt,
		ExitAt:          exitAt,
		GrossPnL:        cp.RealizedPnL,
		Commission:      decimalToString(commission),
		StopLossPrice:   cp.StopLossPrice,
		TakeProfitPrice: cp.TakeProfitPrice,
		PlannedRisk:     plannedRisk(cp.Qty, cp.EntryPrice, cp.StopLossPrice),
		EntryOrderID:    cp.EntryOrderID,
		ExitOrderID:     cp.ExitOrderID,
		NEntries:        cp.NEntries,
		ExitReason:      cp.ExitReason,
		Strategy:        h.StrategyName,
	}
	if err := tl.Append(ctx, rec); err != nil {
		slog.Warn("trade_log: publish failed", "hand_id", h.id, "err", err)
	}
}

// decimalToString returns "" for the zero value so the JSON wire format omits
// the field (matching the omitzero hints on TradeRecord).
func decimalToString(d decimal.Decimal) string {
	if d.IsZero() {
		return ""
	}
	return d.String()
}

// plannedRisk computes the dollar risk implied at entry: qty × |entry − stop_loss|.
// Returns "" when any input is missing — PG sees NULL and r_multiple stays NULL.
func plannedRisk(qtyStr, entryStr, stopStr string) string {
	if qtyStr == "" || entryStr == "" || stopStr == "" {
		return ""
	}
	qty, err1 := decimal.NewFromString(qtyStr)
	entry, err2 := decimal.NewFromString(entryStr)
	stop, err3 := decimal.NewFromString(stopStr)
	if err1 != nil || err2 != nil || err3 != nil {
		return ""
	}
	risk := entry.Sub(stop).Abs().Mul(qty.Abs())
	if !risk.IsPositive() {
		return ""
	}
	return risk.String()
}

// publishBracketPlaced writes KindBracketPlaced to the durable poslog so bracket
// order IDs survive a restart. Called from within the PlaceExitOrders goroutine.
// positionID is the leg's opening order_id (stable PositionID); orderIDs are the
// exchange-returned SL/TP order IDs.
func (h *Hand) publishBracketPlaced(ctx context.Context, positionID, symbol string, orderIDs []string) {
	if len(orderIDs) == 0 {
		return
	}
	payload, err := json.Marshal(poslog.BracketPlacedPayload{
		Symbol:   symbol,
		OrderIDs: orderIDs,
	})
	if err != nil {
		slog.Error("publishBracketPlaced: marshal failed", "hand_id", h.id, "err", err)
		return
	}
	h.publishAndApply(ctx, poslog.Event{
		ID:         positionID + "_bracket_" + orderIDs[0],
		HandID:     h.id.String(),
		HelmID:     h.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindBracketPlaced,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}
