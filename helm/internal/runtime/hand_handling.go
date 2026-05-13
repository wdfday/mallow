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
	handdomain "mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/strategy"
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
		case ev := <-b.fillCh:
			b.handleWsFill(ctx, ev)
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
	signalAt := time.Now()
	b.metrics.signalsReceived.Add(1)

	b.recordActivity(ActivityEntry{
		At:        signalAt,
		Code:      CodeSignalReceived,
		Symbol:    sig.Symbol,
		Direction: string(sig.Direction),
		Strength:  sig.Strength,
	})

	dispatchLag := signalAt.Sub(sig.ReceivedAt).Truncate(time.Millisecond)
	slog.Debug("signal: hand received",
		"hand_id", b.id,
		"symbol", sig.Symbol,
		"dir", sig.Direction,
		"strength", sig.Strength,
		"dispatch_lag", dispatchLag,
	)

	filtered := func(code int, reason string) {
		b.metrics.signalsFiltered.Add(1)
		b.recordActivity(ActivityEntry{
			At:        time.Now(),
			Code:      code,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Strength:  sig.Strength,
			Reason:    reason,
		})
	}

	if !sig.IsUrgent() && b.signalTTL > 0 && !sig.ReceivedAt.IsZero() && time.Since(sig.ReceivedAt) > b.signalTTL {
		age := time.Since(sig.ReceivedAt).Truncate(time.Millisecond)
		slog.Warn("signal: received but expired",
			"hand_id", b.id, "symbol", sig.Symbol,
			"age", age, "ttl", b.signalTTL,
		)
		filtered(CodeSignalStale, fmt.Sprintf("expired: age %s > ttl %s", age, b.signalTTL))
		return
	}

	b.mu.Lock()
	b.health.LastSignalAt = timePtr(time.Now().UTC())
	if b.health.Status == "stale" {
		b.health.Status = "running"
	}
	b.mu.Unlock()

	if b.rt.IsPaused() {
		filtered(CodeSignalHelmPaused, "helm paused")
		return
	}
	if b.IsPaused() {
		filtered(CodeSignalHandPaused, "hand paused")
		return
	}
	if !b.limiter.Allow() {
		slog.Warn("bot: signal rate limited", "hand_id", b.id, "symbol", sig.Symbol)
		filtered(CodeSignalRateLimited, "rate limited")
		return
	}

	intent := b.strategy.Evaluate(sig)

	if intent.Action == strategy.ActionDoNothing {
		filtered(CodeSignalDoNothing, fmt.Sprintf("strength %.2f below min", sig.Strength))
		return
	}

	if sig.IsUrgent() {
		// Resolve close direction against actual position side.
		if pos := b.rt.Portfolio.GetPosition(sig.Symbol); pos != nil {
			if pos.Qty.IsNegative() {
				intent.Action = strategy.ActionExitShort
			} else if pos.Qty.IsPositive() {
				intent.Action = strategy.ActionExitLong
			}
		}
	}

	// MaxUnits guard: cap concurrent legs (non-pyramid) or pyramid entries (pyramid).
	// Pyramid=true still respects MaxUnits — it limits how many times you can add to the leg.
	isEntry := intent.Action == strategy.ActionEnterLong || intent.Action == strategy.ActionEnterShort
	if isEntry {
		b.mu.RLock()
		count := b.pos.EntryCount()
		b.mu.RUnlock()
		if count >= b.maxUnits {
			slog.Debug("bot: max units reached, skipping entry",
				"hand_id", b.id, "symbol", sig.Symbol, "count", count, "max", b.maxUnits)
			filtered(CodeSignalMaxUnits, fmt.Sprintf("max units reached (%d/%d)", count, b.maxUnits))
			return
		}
	}

	reply := b.rt.ProcessTrade(ctx, TradeProposal{
		BotID:  b.id.String(),
		Symbol: sig.Symbol,
		Intent: intent,
		ATR:    sig.ATR,
	}, b.tactician)

	if !reply.Approved {
		slog.Debug("bot: trade rejected", "hand_id", b.id, "symbol", sig.Symbol, "reason", reply.Reason)
		filtered(CodeSignalRejected, reply.Reason)
		return
	}
	b.metrics.tradesApproved.Add(1)

	// Build pending exit level: resolved post-fill from actual fill price.
	// Exchange-side bracket orders (PlaceExitOrders) are placed in applyFill
	// after the fill price is known — not here with an approximate market price.
	pending := exitLevel{Side: reply.Side}
	if sig.TargetPrice.IsPositive() || sig.StopPrice.IsPositive() {
		if sig.IsOffset {
			pending.IsOffset = true
			pending.StopOffset = sig.StopPrice
			pending.TakeProfitOffset = sig.TargetPrice
		} else {
			pending.StopLoss = sig.StopPrice
			pending.TakeProfit = sig.TargetPrice
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

	isFutures := b.rt.Creds.AccountType == exchange.AccountFuturesUSDM ||
		b.rt.Creds.AccountType == exchange.AccountFuturesCOINM
	isExitOrder := intent.Action == strategy.ActionExitLong || intent.Action == strategy.ActionExitShort

	// Apply leverage/margin type on first entry per symbol for futures hands.
	if isFutures && !isExitOrder {
		b.leverageAppliedMu.Lock()
		if !b.leverageApplied[sig.Symbol] {
			b.leverageApplied[sig.Symbol] = true
			b.leverageAppliedMu.Unlock()
			b.applyFuturesLeverage(ctx, sig.Symbol, b.futuresConfig)
		} else {
			b.leverageAppliedMu.Unlock()
		}
	}

	result, err := b.rt.Exchange.PlaceOrder(ctx, b.rt.Creds, exchange.OrderRequest{
		Symbol:     sig.Symbol,
		Side:       exchange.OrderSide(reply.Side),
		Type:       orderType,
		Qty:        reply.Qty,
		Price:      limitPrice,
		ReduceOnly: isFutures && isExitOrder,
	})
	if err != nil {
		b.metrics.ordersFailed.Add(1)
		b.mu.Lock()
		b.health.LastErrorAt = timePtr(time.Now().UTC())
		b.health.LastError = err.Error()
		b.health.Status = "error"
		b.mu.Unlock()
		slog.Error("bot: order failed", "hand_id", b.id, "symbol", sig.Symbol, "err", err)
		b.recordActivity(ActivityEntry{
			At:        time.Now(),
			Code:      CodeOrderFailed,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Strength:  sig.Strength,
			Side:      reply.Side,
			Qty:       reply.Qty,
			Reason:    err.Error(),
		})
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
	order := handdomain.Order{
		HandId:     b.id.String(),
		HelmId:     b.helmID.String(),
		ID:         result.ID,
		Symbol:     sig.Symbol,
		Side:       reply.Side,
		Qty:        reply.Qty,
		Type:       string(orderType),
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

	slog.Info("bot: order placed",
		"hand_id", b.id,
		"order_id", order.ID,
		"symbol", sig.Symbol,
		"side", reply.Side,
		"qty", reply.Qty,
		"status", result.Status,
		"filled_qty", result.FilledQty,
		"filled_avg", result.FilledAvg,
		"signal_latency", time.Since(signalAt).Truncate(time.Millisecond),
	)
	b.recordActivity(ActivityEntry{
		At:        time.Now(),
		Code:      CodeOrderPlaced,
		Symbol:    sig.Symbol,
		Direction: string(sig.Direction),
		Strength:  sig.Strength,
		OrderID:   result.ID,
		Side:      reply.Side,
		Qty:       reply.Qty,
		Price:     limitPrice,
	})

	if result.Status == "filled" {
		// Synchronous fill from REST response: portfolio not yet updated by WS path.
		b.applyFill(ctx, result.ID, sig.Symbol, reply.Side, result.FilledQty, result.FilledAvg, "ws", false)
	}
}

// handleWsFill processes a fully-filled OrderEvent received via the WS path.
// The registry has already called rt.ReportFill for portfolio accounting,
// so this method only updates hand-level state (metrics, exit levels, poslog).
func (b *Hand) handleWsFill(ctx context.Context, ev exchange.OrderEvent) {
	side := "buy"
	if ev.Side == exchange.Sell {
		side = "sell"
	}
	b.mu.Lock()
	b.seenFills[ev.OrderID] = struct{}{}
	b.mu.Unlock()
	// skipPortfolio=true: registry already called rt.ReportFill for this fill.
	b.applyFill(ctx, ev.OrderID, ev.Symbol, side, ev.FilledQty, ev.FilledAvg, "ws", true)
}

func (b *Hand) applyFill(ctx context.Context, orderID, symbol, side string, qty, price decimal.Decimal, source string, skipPortfolio bool) {
	b.metrics.ordersFilled.Add(1)

	// Promote pending exit level to active (entry filled) or discard (close order).
	// Capture the resolved level for post-fill exchange bracket placement.
	var resolvedEl exitLevel
	var hasExitBracket bool
	b.mu.Lock()
	if el, ok := b.pendingExits[orderID]; ok {
		delete(b.pendingExits, orderID)
		if el.IsOffset && price.IsPositive() {
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
		if el.StopLoss.IsPositive() || el.TakeProfit.IsPositive() {
			resolvedEl = el
			hasExitBracket = true
		}
	}
	b.mu.Unlock()

	// Place exchange-side bracket orders (SL/TP) if the exchange supports it.
	// Runs in a goroutine so it doesn't block the fill handling path.
	// On success, stores the resulting order IDs back into exitLevels so they
	// can be cancelled if the position closes via the other exit leg.
	if hasExitBracket {
		if placer, ok := b.rt.Exchange.(exchange.ExitOrderPlacer); ok {
			exitSide := exchange.Sell
			if resolvedEl.Side == "sell" { // short → buy to close
				exitSide = exchange.Buy
			}
			market := exchange.MarketSpot
			if b.rt.Creds.AccountType == exchange.AccountFuturesUSDM ||
				b.rt.Creds.AccountType == exchange.AccountFuturesCOINM {
				market = exchange.MarketFutures
			}
			go func(el exitLevel) {
				exitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				result, err := placer.PlaceExitOrders(exitCtx, b.rt.Creds, exchange.ExitOrderRequest{
					Symbol:     symbol,
					Market:     market,
					Side:       exitSide,
					Qty:        qty,
					StopLoss:   el.StopLoss,
					TakeProfit: el.TakeProfit,
				})
				if err != nil {
					slog.Error("bot: place exit orders failed", "hand_id", b.id, "symbol", symbol, "err", err)
					return
				}
				slog.Info("bot: exit orders placed", "hand_id", b.id, "symbol", symbol, "order_ids", result.OrderIDs)
				// Store order IDs so they can be cancelled on position close.
				b.mu.Lock()
				if lv, ok := b.exitLevels[symbol]; ok {
					lv.ExchangeOrderIDs = result.OrderIDs
					b.exitLevels[symbol] = lv
				}
				b.mu.Unlock()
			}(resolvedEl)
		}
	}

	if price.IsPositive() {
		if pos := b.rt.Portfolio.GetPosition(symbol); pos != nil && !pos.Qty.IsZero() {
			var pnl decimal.Decimal
			if side == "sell" && pos.Qty.IsPositive() {
				pnl = price.Sub(pos.AvgPrice).Mul(qty)
				b.mu.Lock()
				b.cancelExitOrders(ctx, symbol)
				delete(b.exitLevels, symbol)
				b.mu.Unlock()
			} else if side == "buy" && pos.Qty.IsNegative() {
				pnl = pos.AvgPrice.Sub(price).Mul(qty)
				b.mu.Lock()
				b.cancelExitOrders(ctx, symbol)
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

	if !skipPortfolio {
		b.rt.ReportFill(helmdomain.FillReport{
			BotID:          b.id.String(),
			OrchestratorID: b.helmID.String(),
			OrderID:        orderID,
			Symbol:         symbol,
			Side:           side,
			Qty:            qty,
			Price:          price,
			Timestamp:      time.Now().UTC(),
		})
	}

	b.recordActivity(ActivityEntry{
		At:      time.Now(),
		Code:    CodeOrderFilled,
		Symbol:  symbol,
		OrderID: orderID,
		Side:    side,
		Qty:     qty,
		Price:   price,
		Reason:  source,
	})

	// Publish order_filled to the durable position event log.
	b.publishOrderFilled(ctx, orderID, qty, price, source)
}

func (b *Hand) pollOrders(ctx context.Context) {
	b.mu.RLock()
	var pending []handdomain.Order
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
			b.mu.RLock()
			_, alreadySeen := b.seenFills[result.ID]
			b.mu.RUnlock()
			if alreadySeen {
				// WS path already applied this fill — only update order status.
				break
			}
			b.applyFill(ctx, result.ID, result.Symbol, side, result.FilledQty, result.FilledAvg, "poll", false)
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
	dir := strategy.DirLong
	if side == "sell" {
		dir = strategy.DirExit
	}
	signal := strategy.Signal{
		Symbol:     symbol,
		Direction:  dir,
		Strength:   1.0,
		ReceivedAt: time.Now().UTC(),
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
	order := handdomain.Order{
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
		b.applyFill(ctx, result.ID, symbol, side, result.FilledQty, result.FilledAvg, "ws", false)
	}
	return order.ID, nil
}
