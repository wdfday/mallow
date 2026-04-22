package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
	botdomain "orchestrator/internal/module/bot/domain"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/runtime/core/orderbook"
	"orchestrator/internal/runtime/core/strategy"
)

func (b *Bot) run(ctx context.Context) {
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

func (b *Bot) checkStale() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running || b.health.LastSignalAt == nil {
		return
	}
	if time.Since(*b.health.LastSignalAt) > 5*time.Minute {
		b.health.Status = "stale"
	}
}

func (b *Bot) handleSignal(ctx context.Context, sig Signal) {
	b.metrics.signalsReceived.Add(1)

	if !sig.IsUrgent() && !sig.ReceivedAt.IsZero() && time.Since(sig.ReceivedAt) > signalMaxAge {
		b.metrics.signalsFiltered.Add(1)
		slog.Debug("bot: stale signal discarded", "bot_id", b.id, "symbol", sig.Symbol, "age", time.Since(sig.ReceivedAt).Truncate(time.Millisecond))
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
		slog.Warn("bot: signal rate limited", "bot_id", b.id, "symbol", sig.Symbol)
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
				slog.Info("bot: time-stop triggered", "bot_id", b.id, "symbol", sig.Symbol, "bars", bars)
			}
		}
	}

	reply := b.rt.ProcessTrade(ctx, TradeProposal{
		BotID:  b.id,
		Symbol: sig.Symbol,
		Intent: intent,
		ATR:    decimal.NewFromFloat(sig.ATR),
	}, b.tactician)

	if !reply.Approved {
		b.metrics.signalsFiltered.Add(1)
		slog.Debug("bot: trade rejected", "bot_id", b.id, "symbol", sig.Symbol, "reason", reply.Reason)
		return
	}
	b.metrics.tradesApproved.Add(1)

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
		slog.Error("bot: order failed", "bot_id", b.id, "symbol", sig.Symbol, "err", err)
		return
	}
	b.metrics.ordersPlaced.Add(1)

	b.trackOrder(orderbook.PendingOrder{
		OrderID:        result.ID,
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		Symbol:         sig.Symbol,
		Side:           orderbook.OrderSide(reply.Side),
		Qty:            reply.Qty,
	})

	if reply.StopLoss.IsPositive() || reply.TakeProfit.IsPositive() {
		b.mu.Lock()
		b.pendingExits[result.ID] = exitLevel{
			Side:       reply.Side,
			StopLoss:   reply.StopLoss,
			TakeProfit: reply.TakeProfit,
		}
		b.mu.Unlock()
	}

	now := time.Now().UTC()
	order := botdomain.Order{
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		ID:             result.ID,
		Symbol:         sig.Symbol,
		Side:           reply.Side,
		Qty:            reply.Qty,
		Type:           "market",
		Status:         result.Status,
		FilledQty:      result.FilledQty,
		FilledAvg:      result.FilledAvg,
		SubmitTime:     now,
	}
	b.mu.Lock()
	b.orders = append(b.orders, order)
	b.health.LastOrderAt = timePtr(now)
	if b.health.Status == "error" {
		b.health.Status = "running"
	}
	b.mu.Unlock()

	slog.Info("bot: order placed", "bot_id", b.id, "order_id", order.ID,
		"symbol", sig.Symbol, "side", reply.Side, "qty", reply.Qty)

	if result.Status == "filled" {
		b.applyFill(result.ID, sig.Symbol, reply.Side, result.FilledQty, result.FilledAvg)
	}
}

func (b *Bot) applyFill(orderID, symbol, side string, qty, price decimal.Decimal) {
	b.metrics.ordersFilled.Add(1)

	// Promote pending exit level to active (entry filled) or discard (close order).
	b.mu.Lock()
	if el, ok := b.pendingExits[orderID]; ok {
		delete(b.pendingExits, orderID)
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
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		OrderID:        orderID,
		Symbol:         symbol,
		Side:           side,
		Qty:            qty,
		Price:          price,
		Timestamp:      time.Now().UTC(),
	})
}

func (b *Bot) pollOrders(ctx context.Context) {
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
		if result.Status == "filled" {
			side := "buy"
			if result.Side == exchange.Sell {
				side = "sell"
			}
			b.applyFill(result.ID, result.Symbol, side, result.FilledQty, result.FilledAvg)
		}
	}
}

// PlaceOrder submits an order via the full pipeline. Manual/legacy interface.
func (b *Bot) PlaceOrder(ctx context.Context, symbol string, qty decimal.Decimal, side string) (string, error) {
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
		BotID:  b.id,
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
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		Symbol:         symbol,
		Side:           orderbook.OrderSide(side),
		Qty:            reply.Qty,
	})
	order := botdomain.Order{
		BotID:          b.id,
		OrchestratorID: b.orchestratorID,
		ID:             result.ID,
		Symbol:         symbol,
		Side:           side,
		Qty:            reply.Qty,
		Type:           "market",
		Status:         result.Status,
		FilledQty:      result.FilledQty,
		FilledAvg:      result.FilledAvg,
		SubmitTime:     time.Now().UTC(),
	}
	b.mu.Lock()
	b.orders = append(b.orders, order)
	b.health.LastOrderAt = timePtr(time.Now().UTC())
	b.mu.Unlock()
	if result.Status == "filled" {
		b.applyFill(result.ID, symbol, side, result.FilledQty, result.FilledAvg)
	}
	return order.ID, nil
}
