package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/position"
)

func (h *Hand) run(ctx context.Context) {
	defer close(h.done)

	// Tag this goroutine for Pyroscope continuous profiling.
	// CPU/memory profiles will carry hand_id + helm_id labels,
	// enabling per-hand flame graph filtering in the Grafana Pyroscope UI.
	pprof.Do(ctx, pprof.Labels(
		"hand_id", h.id.String(),
		"helm_id", h.helmID.String(),
		"symbol", h.Symbol,
	), func(ctx context.Context) {
		h.runLoop(ctx)
	})
}

// runLoop is the actual select loop; extracted so pprof.Do labels cover the entire goroutine lifetime.
func (h *Hand) runLoop(ctx context.Context) {
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
			h.checkBracketOrders(ctx)
			h.checkPositionDesync(ctx)
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
		if h.health.Status != HealthStale {
			h.health.Status = HealthStale
			h.emitEvent(natsapi.HelmEvent{
				Code:   CodeHandStale,
				Reason: "no signal received in > 5 minutes",
				Msg:    "hand: signal feed stale",
			})
		}
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

	dispatchLag := signalAt.Sub(sig.ReceivedAt).Truncate(time.Millisecond)
	// Track end-to-end lag: GeneratedAt (herald emitted) → now (hand processes).
	// Falls back to NATS delivery lag (ReceivedAt → now) when GeneratedAt is zero.
	if !sig.GeneratedAt.IsZero() {
		h.metrics.latestSignalLagMs.Store(signalAt.Sub(sig.GeneratedAt).Milliseconds())
	} else if dispatchLag > 0 {
		h.metrics.latestSignalLagMs.Store(dispatchLag.Milliseconds())
	}
	receivedReason := fmt.Sprintf("lag=%s strength=%.2f", dispatchLag, sig.Strength)
	if sig.Reason != "" {
		receivedReason += " herald=" + sig.Reason
	}
	slog.Debug("signal: hand received",
		"hand_id", h.id,
		"symbol", sig.Symbol,
		"direction", sig.Direction,
		"strength", sig.Strength,
		"lag", dispatchLag,
		"herald_reason", sig.Reason,
	)
	h.emitEvent(natsapi.HelmEvent{
		Code:      CodeSignalReceived,
		Symbol:    sig.Symbol,
		Direction: string(sig.Direction),
		Reason:    receivedReason,
		Msg:       "signal: hand received",
	})

	filtered := func(code int, reason string) {
		h.metrics.signalsFiltered.Add(1)
		slog.Debug("signal: filtered",
			"hand_id", h.id,
			"symbol", sig.Symbol,
			"direction", sig.Direction,
			"strength", sig.Strength,
			"code", code,
			"reason", reason,
		)
		h.emitEvent(natsapi.HelmEvent{
			Code:      code,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Reason:    reason,
			Msg:       "signal: filtered",
		})
	}

	if !sig.IsUrgent() && h.signalTTL > 0 && !sig.ReceivedAt.IsZero() && time.Since(sig.ReceivedAt) > h.signalTTL {
		age := time.Since(sig.ReceivedAt).Truncate(time.Millisecond)
		reason := fmt.Sprintf("expired: age %s > ttl %s", age, h.signalTTL)
		filtered(CodeSignalStale, reason)
		return
	}

	h.mu.Lock()
	h.health.LastSignalAt = timePtr(time.Now().UTC())
	if h.health.Status == HealthStale {
		h.health.Status = HealthRunning
	}
	h.mu.Unlock()

	if h.helmRuntime.IsPaused() {
		filtered(CodeSignalHelmPaused, "helm paused")
		return
	}
	if h.IsPaused() {
		filtered(CodeSignalHandPaused, "hand paused")
		return
	}
	if !h.limiter.Allow() {
		filtered(CodeSignalRateLimited, "rate limited")
		return
	}

	intent := h.strategy.Evaluate(sig)

	slog.Debug("signal: strategy evaluated",
		"hand_id", h.id,
		"symbol", sig.Symbol,
		"direction", sig.Direction,
		"action", intent.Action,
		"reason", intent.Reason,
	)

	if intent.Action == strategy.ActionDoNothing {
		reason := intent.Reason
		if reason == "" {
			// Fallback: forward herald's own reason if strategy didn't set one.
			if sig.Reason != "" {
				reason = sig.Reason
			} else {
				reason = "strategy: do_nothing"
			}
		}
		filtered(CodeSignalDoNothing, reason)
		return
	}

	if sig.IsUrgent() {
		// Resolve close direction against this hand's own position, not the net
		// helm-level portfolio. Portfolio.GetPosition aggregates all hands on the
		// same symbol, so it would stay non-nil while another hand still holds the
		// asset — causing spurious close orders after this hand is already flat.
		// Also guards against the OCO + checkExits race: if the position was
		// already closed by an OCO fill before this urgent signal is processed,
		// handSide will be "" and we drop the signal rather than placing a spurious
		// closing order (which would short-reverse on a margin account).
		// Only PhaseOpen and PhaseAdding legs have an actual exchange position.
		// PhaseEntering = entry order placed but not yet filled (qty=0 at exchange).
		// PhaseExiting  = close order already in-flight; another exit is redundant.
		h.mu.RLock()
		var handSide string
		for _, leg := range h.pos.ActiveLegs() {
			if leg.Symbol == sig.Symbol &&
				(leg.Phase == position.PhaseOpen || leg.Phase == position.PhaseAdding) {
				handSide = leg.Side
				break
			}
		}
		h.mu.RUnlock()
		if handSide == "" {
			filtered(CodeSignalNoPosition, "exit signal: position already closed")
			return
		}
		if handSide == "sell" {
			intent.Action = strategy.ActionExitShort
		} else {
			intent.Action = strategy.ActionExitLong
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
			filtered(CodeSignalMaxUnits, fmt.Sprintf("max units reached (%d/%d)", count, h.maxUnits))
			return
		}
	}

	// Per-hand qty from poslog — used by tactician for exit/scale-out sizing.
	// Summing Qty across active legs gives the correct per-hand position size,
	// regardless of how many other hands on this helm trade the same symbol.
	h.mu.RLock()
	var handPosQty decimal.Decimal
	for _, leg := range h.pos.ActiveLegs() {
		if leg.Symbol == sig.Symbol {
			handPosQty = handPosQty.Add(leg.Qty)
		}
	}
	h.mu.RUnlock()

	reply := h.helmRuntime.ProcessTrade(ctx, TradeProposal{
		HandID:         h.id.String(),
		Symbol:         sig.Symbol,
		Intent:         intent,
		ATR:            sig.ATR,
		EquityOverride: h.realizedEquity(),
		PositionQty:    handPosQty,
	}, h.tactician)

	slog.Debug("signal: process trade result",
		"hand_id", h.id,
		"symbol", sig.Symbol,
		"approved", reply.Approved,
		"side", reply.Side,
		"qty", reply.Qty,
		"sl", reply.StopLoss,
		"tp", reply.TakeProfit,
		"reason", reply.Reason,
	)

	if !reply.Approved {
		slog.Warn("signal: trade rejected",
			"hand_id", h.id,
			"symbol", sig.Symbol,
			"action", intent.Action,
			"reason", reply.Reason,
		)
		filtered(CodeSignalRejected, reply.Reason)
		h.mu.RLock()
		activeCount := h.pos.ActiveCount()
		h.mu.RUnlock()
		if activeCount == 0 && reply.Reason == "tactics: zero quantity after sizing" {
			slog.Warn("hand: no open positions and cannot size entry — auto-pausing",
				"hand_id", h.id,
				"symbol", sig.Symbol,
				"reason", reply.Reason,
			)
			h.Pause()
		}
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

	isFutures := h.helmRuntime.Creds.AccountType == exchange.AccountFuturesUSDM ||
		h.helmRuntime.Creds.AccountType == exchange.AccountFuturesCOINM
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

	orderQty := reply.Qty
	// Spot exchanges enforce LOT_SIZE step = 0.0001 for most pairs (e.g. ETHUSDT).
	// Truncate to 4 decimal places so qty is always a valid multiple of the step.
	// Futures use ReduceOnly and have their own precision — skip truncation there.
	if !isFutures {
		orderQty = orderQty.Truncate(4)
		// Record sub-step dust so checkPositionDesync doesn't mistake the residual
		// (qty - orderQty) for an external close. Cleared when a new position opens.
		if dust := reply.Qty.Sub(orderQty); dust.IsPositive() {
			h.helmRuntime.RecordDust(sig.Symbol, dust)
		}
	}
	orderReq := exchange.OrderRequest{
		Symbol:     sig.Symbol,
		Side:       exchange.OrderSide(reply.Side),
		Type:       orderType,
		TIF:        exchange.TimeInForce(reply.TIF),
		Qty:        orderQty,
		QuoteQty:   reply.QuoteQty,
		Price:      limitPrice,
		ReduceOnly: isFutures && isExitOrder,
	}
	result, err := h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, orderReq)
	// ── Insufficient-balance retry (spot SELL exit only) ──────────────────────
	// When fee was paid in base asset, poslog may hold gross qty while the wallet
	// holds net (gross - fee). Query actual free balance and retry once.
	if err != nil && isExitOrder && reply.Side == "sell" && !isFutures &&
		isInsufficientBalanceError(err) {
		if bf, ok := h.helmRuntime.Exchange.(exchange.SpotBalanceFetcher); ok {
			baseAsset := spotBaseAsset(sig.Symbol)
			if freeQty, balErr := bf.GetFreeBalance(ctx, h.helmRuntime.Creds, baseAsset); balErr == nil && freeQty.IsPositive() {
				slog.Warn("order: insufficient balance on exit — retrying with actual free balance",
					"hand_id", h.id, "symbol", sig.Symbol,
					"attempted_qty", reply.Qty, "free_qty", freeQty,
				)
				orderReq.Qty = freeQty.Truncate(4)
				result, err = h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, orderReq)
			}
		}
	}
	if err != nil {
		h.metrics.ordersFailed.Add(1)
		h.mu.Lock()
		h.health.LastErrorAt = timePtr(time.Now().UTC())
		h.health.LastError = err.Error()
		h.health.Status = HealthError
		h.mu.Unlock()
		slog.Error("order: placement failed",
			"hand_id", h.id,
			"symbol", sig.Symbol,
			"side", reply.Side,
			"qty", reply.Qty,
			"order_type", orderType,
			"err", err,
		)
		h.emitEvent(natsapi.HelmEvent{
			Code:      CodeOrderFailed,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Side:      reply.Side,
			Qty:       reply.Qty,
			Reason:    err.Error(),
			Msg:       "order: placement failed",
		})
		// Auto-pause when a sizing/lot constraint causes a persistent entry failure.
		// Only pause if this hand has no open position — if we already hold a position
		// the failure is on a scale-in or exit, which should not pause the hand.
		// Use h.pos (per-hand) not Portfolio.GetPosition (net helm) so that another
		// hand holding the same symbol does not suppress the auto-pause.
		if isLotSizeError(err) && !isExitOrder {
			h.mu.RLock()
			flat := h.pos.ActiveCount() == 0
			h.mu.RUnlock()
			if flat {
				h.Pause()
				h.emitEvent(natsapi.HelmEvent{
					Code:   CodeHandAutoPaused,
					Symbol: sig.Symbol,
					Reason: fmt.Sprintf("lot/notional constraint — %s", err.Error()),
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
	// On entry: clear dust residual from the previous trade for this symbol.
	// The old dust no longer matters once a new position is opened.
	if !isExitIntent {
		h.helmRuntime.ClearDust(sig.Symbol)
	}
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

	placedLatency := time.Since(signalAt).Truncate(time.Millisecond)
	placedReason := fmt.Sprintf("status=%s type=%s latency=%s", result.Status, orderType, placedLatency)
	slog.Info("order: placed",
		"hand_id", h.id,
		"symbol", sig.Symbol,
		"side", reply.Side,
		"qty", orderedQty,
		"order_type", orderType,
		"order_id", order.ID,
		"status", result.Status,
		"latency", placedLatency,
	)
	h.emitEvent(natsapi.HelmEvent{
		Code:    CodeOrderPlaced,
		Symbol:  sig.Symbol,
		Side:    reply.Side,
		Qty:     orderedQty,
		Price:   limitPrice,
		OrderID: order.ID,
		Reason:  placedReason,
		Msg:     "order: placed",
	})
	if result.Status == "filled" {
		// Synchronous fill from REST response. Mark as seen so the WS event
		// that will arrive shortly does not double-apply.
		h.mu.Lock()
		h.seenFills[result.ID] = struct{}{}
		h.mu.Unlock()
		// Adjust for base-asset commission: same logic as handleWsFill.
		restQty := result.FilledQty
		if reply.Side == "buy" && result.Commission.IsPositive() && result.CommissionAsset != "" &&
			strings.HasPrefix(result.Symbol, result.CommissionAsset) {
			restQty = restQty.Sub(result.Commission)
		}
		h.applyFill(ctx, result.ID, sig.Symbol, reply.Side, restQty, result.FilledAvg, result.Commission, "rest")
	}
}
