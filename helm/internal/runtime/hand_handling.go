package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
		TIF:        exchange.TimeInForce(reply.TIF),
		Qty:        reply.Qty,
		QuoteQty:   reply.QuoteQty,
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
		// Auto-pause when a sizing/lot constraint causes a persistent entry failure.
		// Only pause if there is no open position — if we already hold a position
		// the failure is on a scale-in or exit, which should not pause the hand.
		if isLotSizeError(err) && !isExitOrder {
			if pos := b.rt.Portfolio.GetPosition(sig.Symbol); pos == nil || pos.Qty.IsZero() {
				b.Pause()
				reason := fmt.Sprintf("auto-paused: lot/notional constraint — %s", err.Error())
				b.recordActivity(ActivityEntry{
					At:     time.Now(),
					Code:   CodeHandAutoPaused,
					Symbol: sig.Symbol,
					Reason: reason,
				})
				slog.Warn("bot: auto-paused due to sizing constraint", "hand_id", b.id, "symbol", sig.Symbol, "err", err)
			}
		}
		return
	}
	b.metrics.ordersPlaced.Add(1)

	// Use exchange-confirmed base qty for tracking; reply.Qty is zero in quote_qty mode.
	orderedQty := reply.Qty
	if !orderedQty.IsPositive() {
		orderedQty = result.Qty
	}
	b.trackOrder(orderbook.PendingOrder{
		OrderID:        result.ID,
		BotID:          b.id.String(),
		OrchestratorID: b.helmID.String(),
		Symbol:         sig.Symbol,
		Side:           orderbook.OrderSide(reply.Side),
		Qty:            orderedQty,
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
		Qty:        orderedQty,
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
			// Capture the hand's lifecycle context so the goroutine exits
			// immediately when Hand.Stop() is called — prevents goroutine leaks
			// when tests or operators shut down a hand during the retry window.
			b.mu.RLock()
			handCtx := b.ctx
			b.mu.RUnlock()

			go func(el exitLevel) {
				// Retry loop: spot exchanges may return "insufficient balance" briefly after
				// a fill if the asset has not yet settled into the available balance.
				// Retries up to 5× with linear backoff (1s, 2s … 5s = 15s total).
				exitReq := exchange.ExitOrderRequest{
					Symbol:     symbol,
					Market:     market,
					Side:       exitSide,
					Qty:        qty,
					StopLoss:   el.StopLoss,
					TakeProfit: el.TakeProfit,
				}
				var result *exchange.ExitOrderResult
				var err error
				for attempt := 1; attempt <= 5; attempt++ {
					select {
					case <-handCtx.Done():
						slog.Info("bot: exit order goroutine cancelled (hand stopped)", "hand_id", b.id, "symbol", symbol)
						return
					case <-time.After(time.Duration(attempt) * time.Second):
					}
					exitCtx, cancel := context.WithTimeout(handCtx, 10*time.Second)
					result, err = placer.PlaceExitOrders(exitCtx, b.rt.Creds, exitReq)
					cancel()
					if err == nil {
						break
					}
					slog.Warn("bot: place exit orders retry", "hand_id", b.id, "symbol", symbol,
						"attempt", attempt, "err", err)
				}
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

		case "new", "accepted", "submitted", "pending_new":
			// Limit order timeout: cancel (and optionally re-place) if it hasn't filled
			// within LimitTimeoutSec seconds.
			if o.Type == "limit" && b.LimitTimeoutSec > 0 {
				timeout := time.Duration(b.LimitTimeoutSec) * time.Second
				if time.Since(o.SubmitTime) > timeout {
					b.handleLimitTimeout(ctx, o, result)
				}
			}

		case "partially_filled":
			// Limit timeout for partially-filled orders (same policy).
			if o.Type == "limit" && b.LimitTimeoutSec > 0 {
				timeout := time.Duration(b.LimitTimeoutSec) * time.Second
				if time.Since(o.SubmitTime) > timeout && result.FilledQty.IsPositive() {
					b.handleLimitTimeout(ctx, o, result)
					break
				}
			}
			// Cancel remainder when the unfilled portion is dust (< 2% of original qty).
			// This prevents tiny open orders that may never fill or fail min-notional.
			if result.FilledQty.IsPositive() && o.Qty.IsPositive() {
				remaining := o.Qty.Sub(result.FilledQty)
				if remaining.IsPositive() && remaining.Div(o.Qty).LessThan(decimal.NewFromFloat(0.02)) {
					if err := b.rt.Exchange.CancelOrder(ctx, b.rt.Creds, o.ID); err != nil {
						slog.Warn("bot: cancel partial remainder failed", "order_id", o.ID, "err", err)
					} else {
						slog.Info("bot: cancelled dust remainder on partial fill",
							"order_id", o.ID, "filled", result.FilledQty, "original", o.Qty)
						b.recordActivity(ActivityEntry{
							At:      time.Now(),
							Code:    CodeOrderPartialCancel,
							Symbol:  result.Symbol,
							OrderID: o.ID,
							Qty:     result.FilledQty,
							Reason:  fmt.Sprintf("remainder %.4f < 2%% of original %.4f — cancelled", remaining.InexactFloat64(), o.Qty.InexactFloat64()),
						})
					}
					side := "buy"
					if result.Side == exchange.Sell {
						side = "sell"
					}
					b.mu.RLock()
					_, alreadySeen := b.seenFills[result.ID]
					b.mu.RUnlock()
					if !alreadySeen {
						b.applyFill(ctx, result.ID, result.Symbol, side, result.FilledQty, result.FilledAvg, "partial_cancel", false)
					}
				}
			}

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

// handleLimitTimeout cancels a stale limit order and, depending on LimitFallback,
// either records a cancel-only event or re-places the remaining qty as a market order.
func (b *Hand) handleLimitTimeout(ctx context.Context, o handdomain.Order, polled *exchange.OrderResult) {
	age := time.Since(o.SubmitTime).Truncate(time.Second)
	if cancelErr := b.rt.Exchange.CancelOrder(ctx, b.rt.Creds, o.ID); cancelErr != nil {
		slog.Warn("bot: limit timeout cancel failed", "order_id", o.ID, "err", cancelErr)
		return
	}

	alreadyFilledQty := polled.FilledQty
	if alreadyFilledQty.IsPositive() {
		// Apply partial fill before re-placing remainder.
		side := "buy"
		if polled.Side == exchange.Sell {
			side = "sell"
		}
		b.mu.RLock()
		_, alreadySeen := b.seenFills[o.ID]
		b.mu.RUnlock()
		if !alreadySeen {
			b.applyFill(ctx, o.ID, polled.Symbol, side, alreadyFilledQty, polled.FilledAvg, "limit_timeout_partial", false)
		}
	}

	remainingQty := o.Qty.Sub(alreadyFilledQty)
	slog.Info("bot: limit order timed out", "order_id", o.ID, "age", age,
		"filled", alreadyFilledQty, "remaining", remainingQty, "fallback", b.LimitFallback)

	b.recordActivity(ActivityEntry{
		At:      time.Now(),
		Code:    CodeOrderLimitTimeout,
		Symbol:  o.Symbol,
		OrderID: o.ID,
		Qty:     remainingQty,
		Reason:  fmt.Sprintf("limit unfilled after %s (filled %s / %s)", age, alreadyFilledQty, o.Qty),
	})

	if b.LimitFallback == "market" && remainingQty.IsPositive() {
		result, err := b.rt.Exchange.PlaceOrder(ctx, b.rt.Creds, exchange.OrderRequest{
			Symbol: o.Symbol,
			Side:   exchange.OrderSide(o.Side),
			Type:   exchange.Market,
			Qty:    remainingQty,
		})
		if err != nil {
			slog.Error("bot: limit fallback market order failed", "order_id", o.ID, "err", err)
			return
		}
		slog.Info("bot: limit fallback market placed", "new_order_id", result.ID, "qty", remainingQty)
		b.recordActivity(ActivityEntry{
			At:      time.Now(),
			Code:    CodeOrderLimitFallback,
			Symbol:  o.Symbol,
			OrderID: result.ID,
			Side:    string(o.Side),
			Qty:     remainingQty,
			Reason:  fmt.Sprintf("fallback from timed-out limit %s", o.ID),
		})
		b.trackOrder(orderbook.PendingOrder{
			OrderID:        result.ID,
			BotID:          b.id.String(),
			OrchestratorID: b.helmID.String(),
			Symbol:         o.Symbol,
			Side:           orderbook.OrderSide(o.Side),
			Qty:            remainingQty,
		})
		if result.Status == "filled" {
			b.applyFill(ctx, result.ID, o.Symbol, string(o.Side), result.FilledQty, result.FilledAvg, "limit_fallback", false)
		}
	}
}

// isLotSizeError returns true when the error is a persistent sizing constraint —
// lot size, min notional, or filter validation — that will recur on every entry
// at the current configured quantity, regardless of market conditions.
func isLotSizeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{
		"lot_size", "min_notional", "notional", "price_filter",
		"lot size", "min notional", "minimum quantity", "minimum amount",
		"filter failure", "below minimum", "invalid quantity",
		"order size", "qty too small", "quantity too small",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
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
