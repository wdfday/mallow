package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/poslog"
	botdomain "mallow/helm/internal/module/hand/domain"
	orchdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/position"
)

func (b *Hand) run(ctx context.Context) {
	defer close(b.done)
	pollTicker := time.NewTicker(5 * time.Second)
	defer pollTicker.Stop()
	staleTicker := time.NewTicker(30 * time.Second)
	defer staleTicker.Stop()

	for {
		select {
		case sig := <-b.UrgentSignals:
			b.handleSignal(ctx, sig)
			continue
		default:
		}

		select {
		case sig := <-b.UrgentSignals:
			b.handleSignal(ctx, sig)
		case sig := <-b.Signals:
			b.handleSignal(ctx, sig)
		case <-pollTicker.C:
			b.pollOrders(ctx)
			b.checkExits()
		case <-staleTicker.C:
			b.checkStale()
		case <-ctx.Done():
			return
		}
	}
}

func (b *Hand) checkStale() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running || b.health.LastSignalAt == nil {
		return
	}
	if time.Since(*b.health.LastSignalAt) > 5*time.Minute {
		b.health.Status = "stale"
	}
}

func (b *Hand) handleSignal(ctx context.Context, sig Signal) {
	b.metrics.signalsReceived.Add(1)

	if !sig.IsUrgent() && !sig.ReceivedAt.IsZero() && time.Since(sig.ReceivedAt) > signalMaxAge {
		b.metrics.signalsFiltered.Add(1)
		slog.Debug("bot: stale signal discarded", "hand_id", b.id, "symbol", sig.Symbol, "age", time.Since(sig.ReceivedAt).Truncate(time.Millisecond))
		return
	}

	b.mu.Lock()
	b.health.LastSignalAt = timePtr(time.Now().UTC())
	if b.health.Status == "stale" {
		b.health.Status = "running"
	}
	b.mu.Unlock()

	if b.rt.IsPaused() || b.IsPaused() {
		b.metrics.signalsFiltered.Add(1)
		return
	}
	if !b.limiter.Allow() {
		b.metrics.signalsFiltered.Add(1)
		slog.Warn("bot: signal rate limited", "hand_id", b.id, "symbol", sig.Symbol)
		return
	}

	signal := strategy.Signal{
		Symbol:    sig.Symbol,
		Direction: sig.Direction,
		Strength:  sig.Strength,
		Timestamp: time.Now().UTC(),
	}
	intent := b.strategy.Evaluate(signal)

	if sig.Direction == "close" {
		if pos := b.rt.Portfolio.GetPosition(sig.Symbol); pos != nil && pos.Qty.IsNegative() {
			intent.Action = strategy.ActionExitShort
		}
	}

	// Time-stop: count each incoming signal as one bar.
	if maxBars := b.tactician.MaxBarsHeld(); maxBars > 0 {
		if pos := b.rt.Portfolio.GetPosition(sig.Symbol); pos != nil && !pos.Qty.IsZero() {
			b.mu.Lock()
			b.barsSinceEntry[sig.Symbol]++
			bars := b.barsSinceEntry[sig.Symbol]
			b.mu.Unlock()
			if bars >= maxBars {
				if pos.Qty.IsPositive() {
					intent.Action = strategy.ActionExitLong
				} else {
					intent.Action = strategy.ActionExitShort
				}
				slog.Info("bot: time-stop triggered", "hand_id", b.id, "symbol", sig.Symbol, "bars", bars)
			}
		}
	}

	reply := b.rt.ProcessTrade(ctx, TradeProposal{
		BotID:  b.id.String(),
		Symbol: sig.Symbol,
		Intent: intent,
		ATR:    decimal.NewFromFloat(sig.ATR),
	}, b.tactician)

	if !reply.Approved {
		b.metrics.signalsFiltered.Add(1)
		slog.Debug("bot: trade rejected", "hand_id", b.id, "symbol", sig.Symbol, "reason", reply.Reason)
		return
	}
	b.metrics.tradesApproved.Add(1)

	// Signal-level SL/TP override tactician config.
	// For is_offset=false: use as absolute prices directly.
	// For is_offset=true: approximate absolute using current market price for the exchange
	// bracket order; the local exit monitor resolves exact levels from actual fill price.
	pending := exitLevel{Side: reply.Side}
	if sig.TargetPrice.IsPositive() || sig.StopPrice.IsPositive() {
		if sig.IsOffset {
			// Resolve approximate absolute prices from current market price for the exchange bracket.
			marketPrice := b.rt.lastKnownPrice(sig.Symbol)
			if marketPrice.IsPositive() {
				if !sig.StopPrice.IsZero() {
					reply.StopLoss = marketPrice.Add(sig.StopPrice)
				}
				if !sig.TargetPrice.IsZero() {
					reply.TakeProfit = marketPrice.Add(sig.TargetPrice)
				}
			}
			// Local monitor resolves exact levels from fill price.
			pending.IsOffset = true
			pending.StopOffset = sig.StopPrice
			pending.TakeProfitOffset = sig.TargetPrice
		} else {
			if sig.StopPrice.IsPositive() {
				reply.StopLoss = sig.StopPrice
			}
			if sig.TargetPrice.IsPositive() {
				reply.TakeProfit = sig.TargetPrice
			}
			pending.StopLoss = reply.StopLoss
			pending.TakeProfit = reply.TakeProfit
		}
	} else {
		pending.StopLoss = reply.StopLoss
		pending.TakeProfit = reply.TakeProfit
	}

	orderType := exchange.Market
	var limitPrice decimal.Decimal
	if reply.EntryType == "limit" && reply.LimitPrice.IsPositive() {
		orderType = exchange.Limit
		limitPrice = reply.LimitPrice
	}

	result, err := b.rt.Exchange.PlaceOrder(ctx, b.rt.Creds, exchange.OrderRequest{
		Symbol:       sig.Symbol,
		Side:         exchange.OrderSide(reply.Side),
		Type:         orderType,
		Qty:          reply.Qty,
		Price:        limitPrice,
		StopLoss:     reply.StopLoss,
		TakeProfit:   reply.TakeProfit,
		TrailingStop: reply.TrailingStop,
	})
	if err != nil {
		b.metrics.ordersFailed.Add(1)
		b.mu.Lock()
		b.health.LastErrorAt = timePtr(time.Now().UTC())
		b.health.LastError = err.Error()
		b.health.Status = "error"
		b.mu.Unlock()
		slog.Error("bot: order failed", "hand_id", b.id, "symbol", sig.Symbol, "err", err)
		return
	}
	b.metrics.ordersPlaced.Add(1)

	b.trackOrder(orderbook.PendingOrder{
		OrderID:        result.ID,
		BotID:          b.id.String(),
		OrchestratorID: b.helmID.String(),
		Symbol:         sig.Symbol,
		Side:           orderbook.OrderSide(reply.Side),
		Qty:            reply.Qty,
	})

	if pending.StopLoss.IsPositive() || pending.TakeProfit.IsPositive() || pending.IsOffset {
		b.mu.Lock()
		b.pendingExits[result.ID] = pending
		b.mu.Unlock()
	}

	// Publish order_placed to the durable position event log.
	isExitIntent := intent.Action == strategy.ActionExitLong || intent.Action == strategy.ActionExitShort
	b.publishOrderPlaced(ctx, result.ID, sig.Symbol, reply, limitPrice, orderType, isExitIntent)

	now := time.Now().UTC()
	order := botdomain.Order{
		HandId:     b.id.String(),
		HelmId:     b.helmID.String(),
		ID:         result.ID,
		Symbol:     sig.Symbol,
		Side:       reply.Side,
		Qty:        reply.Qty,
		Type:       "market",
		Status:     result.Status,
		FilledQty:  result.FilledQty,
		FilledAvg:  result.FilledAvg,
		SubmitTime: now,
	}
	b.mu.Lock()
	b.orders = append(b.orders, order)
	b.health.LastOrderAt = timePtr(now)
	if b.health.Status == "error" {
		b.health.Status = "running"
	}
	b.mu.Unlock()

	slog.Info("bot: order placed", "hand_id", b.id, "order_id", order.ID,
		"symbol", sig.Symbol, "side", reply.Side, "qty", reply.Qty)

	if result.Status == "filled" {
		b.applyFill(ctx, result.ID, sig.Symbol, reply.Side, result.FilledQty, result.FilledAvg, "ws")
	}
}

func (b *Hand) applyFill(ctx context.Context, orderID, symbol, side string, qty, price decimal.Decimal, source string) {
	b.metrics.ordersFilled.Add(1)

	// Promote pending exit level to active (entry filled) or discard (close order).
	b.mu.Lock()
	if el, ok := b.pendingExits[orderID]; ok {
		delete(b.pendingExits, orderID)
		if el.IsOffset && price.IsPositive() {
			// Resolve offsets to absolute prices using actual fill price.
			el.IsOffset = false
			if !el.StopOffset.IsZero() {
				el.StopLoss = price.Add(el.StopOffset)
			}
			if !el.TakeProfitOffset.IsZero() {
				el.TakeProfit = price.Add(el.TakeProfitOffset)
			}
			el.StopOffset = decimal.Zero
			el.TakeProfitOffset = decimal.Zero
		}
		b.exitLevels[symbol] = el
	}
	b.mu.Unlock()

	if price.IsPositive() {
		if pos := b.rt.Portfolio.GetPosition(symbol); pos != nil && !pos.Qty.IsZero() {
			var pnl decimal.Decimal
			if side == "sell" && pos.Qty.IsPositive() {
				pnl = price.Sub(pos.AvgPrice).Mul(qty)
				b.mu.Lock()
				delete(b.barsSinceEntry, symbol)
				delete(b.exitLevels, symbol)
				b.mu.Unlock()
			} else if side == "buy" && pos.Qty.IsNegative() {
				pnl = pos.AvgPrice.Sub(price).Mul(qty)
				b.mu.Lock()
				delete(b.barsSinceEntry, symbol)
				delete(b.exitLevels, symbol)
				b.mu.Unlock()
			}
			if !pnl.IsZero() {
				b.metrics.mu.Lock()
				b.metrics.totalPnL = b.metrics.totalPnL.Add(pnl)
				if pnl.IsPositive() {
					b.metrics.winCount++
				} else {
					b.metrics.lossCount++
				}
				b.metrics.mu.Unlock()
			}
		}
	}

	b.rt.ReportFill(orchdomain.FillReport{
		BotID:          b.id.String(),
		OrchestratorID: b.helmID.String(),
		OrderID:        orderID,
		Symbol:         symbol,
		Side:           side,
		Qty:            qty,
		Price:          price,
		Timestamp:      time.Now().UTC(),
	})

	// Publish order_filled to the durable position event log.
	b.publishOrderFilled(ctx, orderID, qty, price, source)
}

func (b *Hand) pollOrders(ctx context.Context) {
	b.mu.RLock()
	var pending []botdomain.Order
	for _, o := range b.orders {
		switch o.Status {
		case "new", "accepted", "pending_new", "partially_filled", "submitted":
			pending = append(pending, o)
		}
	}
	b.mu.RUnlock()

	for _, o := range pending {
		result, err := b.rt.Exchange.GetOrder(ctx, b.rt.Creds, o.ID)
		if err != nil {
			slog.Warn("bot: poll order failed", "order_id", o.ID, "err", err)
			continue
		}
		b.mu.Lock()
		for i := range b.orders {
			if b.orders[i].ID == o.ID {
				b.orders[i].Status = result.Status
				b.orders[i].FilledQty = result.FilledQty
				b.orders[i].FilledAvg = result.FilledAvg
				break
			}
		}
		b.mu.Unlock()
		switch result.Status {
		case "filled":
			side := "buy"
			if result.Side == exchange.Sell {
				side = "sell"
			}
			b.applyFill(ctx, result.ID, result.Symbol, side, result.FilledQty, result.FilledAvg, "poll")
		case "cancelled", "rejected", "expired":
			b.mu.RLock()
			posID := b.pendingOrderPos[o.ID]
			b.mu.RUnlock()
			if posID != "" {
				payload, _ := json.Marshal(poslog.OrderCancelledPayload{
					OrderID: o.ID,
					Reason:  result.Status,
				})
				b.publishAndApply(ctx, poslog.Event{
					ID:         o.ID + "_cancelled",
					HandID:     b.id.String(),
					HelmID:     b.helmID.String(),
					PositionID: posID,
					Kind:       poslog.KindOrderCancelled,
					Payload:    payload,
					At:         time.Now().UTC(),
				})
			}
		}
	}
}

// PlaceOrder submits an order via the full pipeline. Manual/legacy interface.
func (b *Hand) PlaceOrder(ctx context.Context, symbol string, qty decimal.Decimal, side string) (string, error) {
	if !b.IsRunning() {
		return "", fmt.Errorf("bot is not running")
	}
	if !qty.IsPositive() {
		return "", fmt.Errorf("invalid quantity: %s", qty)
	}
	direction := "long"
	if side == "sell" {
		direction = "close"
	}
	signal := strategy.Signal{
		Symbol:    symbol,
		Direction: direction,
		Strength:  1.0,
		Timestamp: time.Now().UTC(),
	}
	intent := b.strategy.Evaluate(signal)
	reply := b.rt.ProcessTrade(ctx, TradeProposal{
		BotID:  b.id.String(),
		Symbol: symbol,
		Intent: intent,
	}, b.tactician)
	if !reply.Approved {
		return "", fmt.Errorf("trade rejected: %s", reply.Reason)
	}
	result, err := b.rt.Exchange.PlaceOrder(ctx, b.rt.Creds, exchange.OrderRequest{
		Symbol: symbol,
		Side:   exchange.OrderSide(side),
		Type:   exchange.Market,
		Qty:    reply.Qty,
	})
	if err != nil {
		b.metrics.ordersFailed.Add(1)
		return "", fmt.Errorf("place order: %w", err)
	}
	b.metrics.ordersPlaced.Add(1)
	b.trackOrder(orderbook.PendingOrder{
		OrderID:        result.ID,
		BotID:          b.id.String(),
		OrchestratorID: b.helmID.String(),
		Symbol:         symbol,
		Side:           orderbook.OrderSide(side),
		Qty:            reply.Qty,
	})
	order := botdomain.Order{
		HandId:     b.id.String(),
		HelmId:     b.helmID.String(),
		ID:         result.ID,
		Symbol:     symbol,
		Side:       side,
		Qty:        reply.Qty,
		Type:       "market",
		Status:     result.Status,
		FilledQty:  result.FilledQty,
		FilledAvg:  result.FilledAvg,
		SubmitTime: time.Now().UTC(),
	}
	b.mu.Lock()
	b.orders = append(b.orders, order)
	b.health.LastOrderAt = timePtr(time.Now().UTC())
	b.mu.Unlock()
	if result.Status == "filled" {
		b.applyFill(ctx, result.ID, symbol, side, result.FilledQty, result.FilledAvg, "ws")
	}
	return order.ID, nil
}

// ── poslog publish helpers ────────────────────────────────────────────────────

// publishOrderPlaced emits KindOrderPlaced to the durable poslog.
// isExitIntent: true when closing a position (not opening or adding).
func (b *Hand) publishOrderPlaced(
	ctx context.Context,
	orderID, symbol string,
	reply orchdomain.TradeReply,
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
	// This makes the audit log more readable and supports external close detection.
	if isClosingFill {
		// PnL was already computed and accumulated in b.pos by publishAndApply above.
		// We re-derive it here for the payload.
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
