package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/strategy"
)

func (h *Hand) run(ctx context.Context) {
	defer close(h.done)
	pollTicker := time.NewTicker(5 * time.Second)
	defer pollTicker.Stop()
	staleTicker := time.NewTicker(30 * time.Second)
	defer staleTicker.Stop()

	for {
		select {
		case sig := <-h.UrgentSignals:
			h.handleSignal(ctx, sig)
			continue
		default:
		}

		select {
		case sig := <-h.UrgentSignals:
			h.handleSignal(ctx, sig)
		case sig := <-h.Signals:
			h.handleSignal(ctx, sig)
		case ev := <-h.fillCh:
			h.handleWsFill(ctx, ev)
		case <-pollTicker.C:
			h.pollOrders(ctx)
			h.checkExits()
		case <-staleTicker.C:
			h.checkStale()
		case <-ctx.Done():
			return
		}
	}
}

func (h *Hand) checkStale() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.running || h.health.LastSignalAt == nil {
		return
	}
	if time.Since(*h.health.LastSignalAt) > 5*time.Minute {
		h.health.Status = HealthStale
	}

	// Trim seenFills: remove entries for orders that have reached a terminal state
	// (filled/canceled/rejected/expired). Terminal orders are never re-polled, so
	// their seenFills entries serve no purpose and would grow unboundedly.
	if len(h.seenFills) > 100 {
		live := make(map[string]struct{}, len(h.orders))
		for _, o := range h.orders {
			switch o.Status {
			case "new", "accepted", "pending_new", "partially_filled", "submitted":
				live[o.ID] = struct{}{}
			}
		}
		for id := range h.seenFills {
			if _, stillLive := live[id]; !stillLive {
				delete(h.seenFills, id)
			}
		}
	}
}

func (h *Hand) handleSignal(ctx context.Context, sig Signal) {
	signalAt := time.Now()
	h.metrics.signalsReceived.Add(1)

	h.recordActivity(ActivityEntry{
		At:        signalAt,
		Code:      CodeSignalReceived,
		Symbol:    sig.Symbol,
		Direction: string(sig.Direction),
		Strength:  sig.Strength,
	})

	dispatchLag := signalAt.Sub(sig.ReceivedAt).Truncate(time.Millisecond)
	h.rt.EmitEvent(natsapi.HelmEvent{
		HandID:    h.id.String(),
		Code:      CodeSignalReceived,
		Symbol:    sig.Symbol,
		Direction: string(sig.Direction),
		Reason:    fmt.Sprintf("lag=%s", dispatchLag),
		Msg:       "signal: hand received",
	})

	filtered := func(code int, reason string) {
		h.metrics.signalsFiltered.Add(1)
		h.recordActivity(ActivityEntry{
			At:        time.Now(),
			Code:      code,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Strength:  sig.Strength,
			Reason:    reason,
		})
	}

	if !sig.IsUrgent() && h.signalTTL > 0 && !sig.ReceivedAt.IsZero() && time.Since(sig.ReceivedAt) > h.signalTTL {
		age := time.Since(sig.ReceivedAt).Truncate(time.Millisecond)
		reason := fmt.Sprintf("expired: age %s > ttl %s", age, h.signalTTL)
		h.rt.EmitEvent(natsapi.HelmEvent{
			HandID:    h.id.String(),
			Code:      CodeSignalStale,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Reason:    reason,
			Msg:       "signal: stale — dropped",
		})
		filtered(CodeSignalStale, reason)
		return
	}

	h.mu.Lock()
	h.health.LastSignalAt = timePtr(time.Now().UTC())
	if h.health.Status == HealthStale {
		h.health.Status = HealthRunning
	}
	h.mu.Unlock()

	if h.rt.IsPaused() {
		filtered(CodeSignalHelmPaused, "helm paused")
		return
	}
	if h.IsPaused() {
		filtered(CodeSignalHandPaused, "hand paused")
		return
	}
	if !h.limiter.Allow() {
		h.rt.EmitEvent(natsapi.HelmEvent{
			HandID: h.id.String(),
			Code:   CodeSignalRateLimited,
			Symbol: sig.Symbol,
			Msg:    "signal: rate limited",
		})
		filtered(CodeSignalRateLimited, "rate limited")
		return
	}

	intent := h.strategy.Evaluate(sig)

	if intent.Action == strategy.ActionDoNothing {
		filtered(CodeSignalDoNothing, fmt.Sprintf("strength %.2f below min", sig.Strength))
		return
	}

	if sig.IsUrgent() {
		// Resolve close direction against actual position side.
		if pos := h.rt.Portfolio.GetPosition(sig.Symbol); pos != nil {
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
		h.mu.RLock()
		count := h.pos.EntryCount()
		h.mu.RUnlock()
		if count >= h.maxUnits {
			reason := fmt.Sprintf("max units reached (%d/%d)", count, h.maxUnits)
			h.rt.EmitEvent(natsapi.HelmEvent{
				HandID: h.id.String(),
				Code:   CodeSignalMaxUnits,
				Symbol: sig.Symbol,
				Reason: reason,
				Msg:    "signal: max units — entry skipped",
			})
			filtered(CodeSignalMaxUnits, reason)
			return
		}
	}

	reply := h.rt.ProcessTrade(ctx, TradeProposal{
		HandID:         h.id.String(),
		Symbol:         sig.Symbol,
		Intent:         intent,
		ATR:            sig.ATR,
		EquityOverride: h.realizedEquity(),
	}, h.tactician)

	if !reply.Approved {
		h.rt.EmitEvent(natsapi.HelmEvent{
			HandID: h.id.String(),
			Code:   CodeSignalRejected,
			Symbol: sig.Symbol,
			Reason: reply.Reason,
			Msg:    "signal: trade rejected by risk manager",
		})
		filtered(CodeSignalRejected, reply.Reason)
		return
	}
	h.metrics.tradesApproved.Add(1)

	// Build pending exit level: resolved post-fill from actual fill price.
	// Exchange-side bracket orders (PlaceExitOrders) are placed in applyFill
	// after the fill price is known — not here with an approximate market price.
	pending := exitPending{Side: reply.Side}
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

	// Hand-level OrderType is the default; signal's EntryType can override to limit.
	orderType := exchange.Market
	if h.orderType == handdomain.OrderTypeLimit {
		orderType = exchange.Limit
	}
	var limitPrice decimal.Decimal
	if reply.EntryType == "limit" && reply.LimitPrice.IsPositive() {
		orderType = exchange.Limit
		limitPrice = reply.LimitPrice
	}

	isFutures := h.rt.Creds.AccountType == exchange.AccountFuturesUSDM ||
		h.rt.Creds.AccountType == exchange.AccountFuturesCOINM
	isExitOrder := intent.Action == strategy.ActionExitLong || intent.Action == strategy.ActionExitShort

	// Apply leverage/margin type on first entry per symbol for futures hands.
	if isFutures && !isExitOrder {
		h.leverageAppliedMu.Lock()
		if !h.leverageApplied[sig.Symbol] {
			h.leverageApplied[sig.Symbol] = true
			h.leverageAppliedMu.Unlock()
			h.applyFuturesLeverage(ctx, sig.Symbol, h.futuresConfig)
		} else {
			h.leverageAppliedMu.Unlock()
		}
	}

	result, err := h.rt.Exchange.PlaceOrder(ctx, h.rt.Creds, exchange.OrderRequest{
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
		h.metrics.ordersFailed.Add(1)
		h.mu.Lock()
		h.health.LastErrorAt = timePtr(time.Now().UTC())
		h.health.LastError = err.Error()
		h.health.Status = HealthError
		h.mu.Unlock()
		h.rt.EmitEvent(natsapi.HelmEvent{
			HandID:    h.id.String(),
			Code:      CodeOrderFailed,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Side:      reply.Side,
			Qty:       reply.Qty,
			Reason:    err.Error(),
			Msg:       "order: placement failed",
		})
		h.recordActivity(ActivityEntry{
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
			if pos := h.rt.Portfolio.GetPosition(sig.Symbol); pos == nil || pos.Qty.IsZero() {
				h.Pause()
				reason := fmt.Sprintf("auto-paused: lot/notional constraint — %s", err.Error())
				h.recordActivity(ActivityEntry{
					At:     time.Now(),
					Code:   CodeHandAutoPaused,
					Symbol: sig.Symbol,
					Reason: reason,
				})
				h.rt.EmitEvent(natsapi.HelmEvent{
					HandID: h.id.String(),
					Code:   CodeHandAutoPaused,
					Symbol: sig.Symbol,
					Reason: reason,
					Msg:    "hand: auto-paused due to sizing constraint",
				})
			}
		}
		return
	}
	h.metrics.ordersPlaced.Add(1)

	// Use exchange-confirmed base qty for tracking; reply.Qty is zero in quote_qty mode.
	orderedQty := reply.Qty
	if !orderedQty.IsPositive() {
		orderedQty = result.Qty
	}
	h.trackOrder(result.ID)

	if pending.StopLoss.IsPositive() || pending.TakeProfit.IsPositive() || pending.IsOffset {
		h.mu.Lock()
		h.pendingExits[result.ID] = pending
		h.mu.Unlock()
	}

	// Publish order_placed to the durable position event log.
	isExitIntent := intent.Action == strategy.ActionExitLong || intent.Action == strategy.ActionExitShort
	h.publishOrderPlaced(ctx, result.ID, sig.Symbol, reply, limitPrice, orderType, isExitIntent, sig.PatternKind)

	now := time.Now().UTC()
	order := handdomain.Order{
		HandId:     h.id.String(),
		HelmId:     h.helmID.String(),
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
	h.mu.Lock()
	h.orders = append(h.orders, order)
	h.health.LastOrderAt = timePtr(now)
	if h.health.Status == HealthError {
		h.health.Status = HealthRunning
	}
	h.mu.Unlock()

	h.rt.EmitEvent(natsapi.HelmEvent{
		HandID:  h.id.String(),
		Code:    CodeOrderPlaced,
		Symbol:  sig.Symbol,
		Side:    reply.Side,
		Qty:     orderedQty,
		Price:   limitPrice,
		OrderID: order.ID,
		Reason:  fmt.Sprintf("status=%s latency=%s", result.Status, time.Since(signalAt).Truncate(time.Millisecond)),
		Msg:     "order: placed",
	})
	h.recordActivity(ActivityEntry{
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
		// Synchronous fill from REST response. Mark as seen so the WS event
		// that will arrive shortly does not double-apply.
		h.mu.Lock()
		h.seenFills[result.ID] = struct{}{}
		h.mu.Unlock()
		h.applyFill(ctx, result.ID, sig.Symbol, reply.Side, result.FilledQty, result.FilledAvg, "rest")
	}
}
