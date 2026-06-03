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
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/clid"
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
		// Priority drain: exit signals and fills jump ahead of the poll/bracket-state
		// results. Fills-before-pollCh is a CORRECTNESS requirement: when an OCO take-profit
		// fills, the exchange auto-cancels the paired stop-loss leg. If applyBracketStates
		// (from pollCh) processes that cancelled SL before the TP fill is booked, the leg is
		// disowned (orphaned, no PnL) instead of closed — losing the winning trade's PnL (#4).
		// Draining fills first guarantees the close is booked, so the SL-cancel then finds the
		// leg already gone and is correctly ignored.
		select {
		case sig := <-h.UrgentSignals:
			h.handleSignal(ctx, sig)
			continue
		case <-h.fillSignal:
			h.drainFills(ctx)
			continue
		default:
		}

		select {
		case sig := <-h.UrgentSignals:
			h.handleSignal(ctx, sig)
		case sig := <-h.Signals:
			h.handleSignal(ctx, sig)
		case <-h.fillSignal:
			h.drainFills(ctx)
		case pp := <-h.placeResultCh:
			// Off-loop order placement came back — record/poslog on success, clean up on
			// failure, all on the loop (single-owner).
			h.applyPlaceResult(ctx, pp)
		case batch := <-h.pollCh:
			// Off-loop poll batch came back — apply its state transitions on the loop.
			h.pollInFlight = false
			h.applyPolledOrders(ctx, batch.orders)
			h.applyBracketStates(ctx, batch.brackets)
		case <-pollTicker.C:
			// Fan out the order + bracket GetOrder calls OFF the loop so a slow poll can't
			// starve the fill mailbox; one batch in flight at a time (pollInFlight guard).
			if !h.pollInFlight {
				h.pollInFlight = true
				go func() {
					batch := pollBatch{
						orders:   h.fetchPendingOrders(ctx),
						brackets: h.fetchBracketStates(ctx),
					}
					select {
					case h.pollCh <- batch:
					case <-ctx.Done():
					}
				}()
			}
			h.checkExits()             // mostly in-memory; conditional REST already off-loaded
			h.checkPositionDesync(ctx) // in-memory portfolio compare, no REST
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

	// Trim seenFills and partialApplied: remove entries for orders that have reached
	// a terminal state (filled/canceled/rejected/expired). Terminal orders are never
	// re-polled, so these entries serve no purpose and would grow unboundedly.
	if len(h.seenFills) > 100 || len(h.partialApplied) > 50 {
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
		// partialApplied entries should be consumed (deleted) when the order reaches a
		// terminal state in pollOrders. This is a safety net for edge cases where the
		// order was removed from h.orders without going through the normal terminal path.
		for id := range h.partialApplied {
			if _, stillLive := live[id]; !stillLive {
				delete(h.partialApplied, id)
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

	if !sig.IsUrgent() && h.cfg.signalTTL > 0 && !sig.ReceivedAt.IsZero() && time.Since(sig.ReceivedAt) > h.cfg.signalTTL {
		age := time.Since(sig.ReceivedAt).Truncate(time.Millisecond)
		reason := fmt.Sprintf("expired: age %s > ttl %s", age, h.cfg.signalTTL)
		filtered(CodeSignalStale, reason)
		return
	}

	h.mu.Lock()
	h.health.LastSignalAt = new(time.Now().UTC())
	h.mu.Unlock()

	if h.helmRuntime.IsPaused() {
		filtered(CodeSignalHelmPaused, "helm paused")
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
		if count >= h.cfg.maxUnits {
			filtered(CodeSignalMaxUnits, fmt.Sprintf("max units reached (%d/%d)", count, h.cfg.maxUnits))
			return
		}
	}

	// Pyramiding price gate (avg-anchored): only ADD to an existing leg when it is winning —
	// current price beyond the blended avg entry. Presses winners and blocks averaging-down even
	// if the script re-signals on a flat/adverse bar (engine does not trust re-signal blindly).
	// The anchor is the leg avg (not the last unit), matching the avg-anchored SL/TP rebase.
	// First entry (flat) is never gated. Fail-closed: a missing price blocks the add.
	// Scope: pyramid (merge) mode; independent-leg (OFF) gating is out of scope for now.
	// See docs/pyramiding-design.md (avg-anchor decision). PARITY TODO: engine still gates on
	// last_unit_price — migrate engine to avg to keep backtest ↔ live identical.
	if isEntry && h.cfg.pyramid {
		h.mu.RLock()
		var legSide string
		var legAvg, legQty decimal.Decimal
		if leg := h.pos.PrimaryLeg(); leg != nil {
			legSide, legAvg, legQty = leg.Side, leg.EntryPrice, leg.Qty
		}
		h.mu.RUnlock()
		if legQty.IsPositive() {
			px := h.helmRuntime.lastKnownPrice(sig.Symbol)
			winning := px.IsPositive() &&
				((legSide == "buy" && px.GreaterThan(legAvg)) ||
					(legSide == "sell" && px.LessThan(legAvg)))
			if !winning {
				filtered(CodeSignalDoNothing, fmt.Sprintf(
					"pyramid gate: price %s not beyond avg entry %s (%s) — leg not winning, add blocked",
					px, legAvg, legSide))
				return
			}
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

	// Capital isolation:
	//   - Allocated hand (allocatedCap > 0): override equity with hand's own
	//     realized equity so sizing tracks the hand's PnL, not the shared pool.
	//     Also pass AvailableBudget so the tactician can hard-clamp qty to the
	//     remaining budget (allocated + cumPnL − deployedCapital).
	//   - Shared-pool hand (allocatedCap = 0): leave both zero so the tactician
	//     falls back to portfolio equity and is bounded only by helm-level risk.
	var equityOverride, availableBudget decimal.Decimal
	if h.AllocatedCapital().IsPositive() {
		equityOverride = h.realizedEquity()
		availableBudget = h.AvailableCash()
	}

	reply := h.helmRuntime.ProcessTrade(ctx, TradeProposal{
		HandID:          h.id.String(),
		Symbol:          sig.Symbol,
		Intent:          intent,
		ATR:             sig.ATR,
		EquityOverride:  equityOverride,
		AvailableBudget: availableBudget,
		PositionQty:     handPosQty,
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
			slog.Warn("hand: no open positions and cannot size entry — auto-stopping",
				"hand_id", h.id,
				"symbol", sig.Symbol,
				"reason", reply.Reason,
			)
			h.emitEvent(natsapi.HelmEvent{
				Code:   CodeHandAutoStopped,
				Symbol: sig.Symbol,
				Reason: reply.Reason,
				Msg:    "hand: auto-stopped — cannot size entry with zero open positions",
			})
			go h.Stop()
		}
		return
	}
	h.metrics.tradesApproved.Add(1)

	// Inject a default SL for entry orders that have no explicit stop loss.
	// Prevents naked entries while still letting the user omit sl in their script.
	// Applied before publishOrderPlaced so the poslog payload captures the injected level.
	if isEntry && reply.StopLoss.IsZero() && sig.StopPrice.IsZero() {
		entryPrice := h.helmRuntime.lastKnownPrice(sig.Symbol)
		if entryPrice.IsPositive() {
			reply.StopLoss = computeDefaultSL(reply.Side, entryPrice, sig.ATR)
			slog.Info("hand: default SL injected",
				"hand_id", h.id,
				"symbol", sig.Symbol,
				"side", reply.Side,
				"sl", reply.StopLoss,
				"atr", sig.ATR,
				"entry_price", entryPrice,
			)
		}
	}

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

	// Hand config is authoritative for market vs limit.
	// A market hand always executes at market; a limit hand uses the tactician's price.
	// The signal's entry_type is a hint for limit hands only — it never overrides a
	// market hand config (signal strength → UrgencyNormal would otherwise silently
	// convert every 0.5–0.8 strength signal to a limit order on a market hand).
	var limitPrice decimal.Decimal
	orderType := exchange.Market
	if h.cfg.orderType == handdomain.OrderTypeLimit {
		orderType = exchange.Limit
		if reply.EntryType == "limit" && reply.LimitPrice.IsPositive() {
			limitPrice = reply.LimitPrice
		}
		// If no limit price resolved, fall back to market to avoid Price("0") → PRICE_FILTER.
		if limitPrice.IsZero() {
			orderType = exchange.Market
		}
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
			h.applyFuturesLeverage(ctx, sig.Symbol, h.cfg.futuresConfig)
		} else {
			h.leverageAppliedMu.Unlock()
		}
	}

	orderQty := reply.Qty
	// Truncate qty to the exchange's LOT_SIZE stepSize so the order is a valid
	// multiple of the step. Futures use ReduceOnly and have their own precision.
	if !isFutures {
		orderQty = truncateQty(h.helmRuntime.filtersFor(ctx, sig.Symbol), orderQty)
		// Record sub-step dust so checkPositionDesync doesn't mistake the residual
		// (qty - orderQty) for an external close. Cleared when a new position opens.
		if dust := reply.Qty.Sub(orderQty); dust.IsPositive() {
			h.helmRuntime.RecordDust(sig.Symbol, dust)
		}
	}
	// If truncation rounded the qty to zero, the entire position is sub-step dust.
	// Close the poslog leg without placing an exchange order — the tiny remainder
	// stays in the helm-level portfolio (not the hand's concern).
	if isExitOrder && !isFutures && orderQty.IsZero() {
		slog.Info("order: exit qty rounded to zero — dust exit (no exchange order placed)",
			"hand_id", h.id, "symbol", sig.Symbol, "original_qty", reply.Qty)
		h.emitEvent(natsapi.HelmEvent{
			Code:   CodeOrderDustExit,
			Symbol: sig.Symbol,
			Qty:    reply.Qty,
			Reason: fmt.Sprintf("exit qty %s rounds to zero after truncation — dust_exit", reply.Qty),
			Msg:    "order: sub-step dust exit — poslog closed without exchange sell",
		})
		h.helmRuntime.RecordDust(sig.Symbol, reply.Qty)
		lastPrice := h.helmRuntime.lastKnownPrice(sig.Symbol)
		h.closeLegAsDust(ctx, sig.Symbol, reply.Side, reply.Qty, lastPrice)
		return
	}
	// Generate the client order id and track it BEFORE placing the order, so a WS
	// fill that races ahead of the REST response still routes to this hand via the
	// clid (the exchange does not know it yet, but we already do). See CLIENT_ORDER_ID.md.
	clid := clid.New()
	orderReq := exchange.OrderRequest{
		Symbol:        sig.Symbol,
		Side:          exchange.OrderSide(reply.Side),
		Type:          orderType,
		TIF:           exchange.TimeInForce(reply.TIF),
		Qty:           orderQty,
		QuoteQty:      reply.QuoteQty,
		Price:         limitPrice,
		ReduceOnly:    isFutures && isExitOrder,
		ClientOrderID: clid,
	}
	h.trackOrder(clid)
	// Place the order OFF the loop. The REST (PlaceOrder + balance-retry + ambiguous
	// recovery) can take 100ms–2s; blocking the loop here would delay draining the fill
	// mailbox and placing brackets for in-flight positions. The fill routes by the
	// pre-tracked clid regardless of when the REST returns, so the result only comes back to
	// the loop for bookkeeping (applyPlaceResult) via placeResultCh.
	pp := &pendingPlace{
		sig: sig, intent: intent, reply: reply, pending: pending, clid: clid,
		orderReq: orderReq, orderType: orderType, limitPrice: limitPrice, orderQty: orderQty,
		isFutures: isFutures, isExitOrder: isExitOrder, signalAt: signalAt,
	}
	go func() {
		h.runPlaceREST(ctx, pp)
		select {
		case h.placeResultCh <- pp:
		case <-ctx.Done():
		}
	}()
}

// pendingPlace carries an entry/exit placement across the off-loop REST boundary.
// The run loop builds it (after sizing + clid tracking), a goroutine fills result/err via
// runPlaceREST, and the loop finishes the bookkeeping via applyPlaceResult.
type pendingPlace struct {
	sig         Signal
	intent      strategy.Intent
	reply       helmdomain.TradeReply
	pending     exitPending
	clid        string
	orderReq    exchange.OrderRequest
	orderType   exchange.OrderType
	limitPrice  decimal.Decimal
	orderQty    decimal.Decimal
	isFutures   bool
	isExitOrder bool
	signalAt    time.Time

	result *exchange.OrderResult // set by runPlaceREST
	err    error
}

// runPlaceREST is the I/O phase of an entry/exit: PlaceOrder plus the insufficient-balance
// retry and the ambiguous-failure (clid) recovery. Pure REST — it mutates no hand state
// (the clid was tracked on-loop before this runs) — so it executes OFF the actor loop.
func (h *Hand) runPlaceREST(ctx context.Context, pp *pendingPlace) {
	result, err := h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, pp.orderReq)
	// ── Insufficient-balance retry (spot SELL exit only) ──────────────────────
	// When fee was paid in base asset, poslog may hold gross qty while the wallet
	// holds net (gross - fee). Query actual free balance and retry once.
	if err != nil && pp.isExitOrder && pp.reply.Side == "sell" && !pp.isFutures &&
		isInsufficientBalanceError(err) {
		if bf, ok := h.helmRuntime.Exchange.(exchange.SpotBalanceFetcher); ok {
			baseAsset := spotBaseAsset(pp.sig.Symbol)
			if freeQty, balErr := bf.GetFreeBalance(ctx, h.helmRuntime.Creds, baseAsset); balErr == nil && freeQty.IsPositive() {
				slog.Warn("order: insufficient balance on exit — retrying with actual free balance",
					"hand_id", h.id, "symbol", pp.sig.Symbol,
					"attempted_qty", pp.reply.Qty, "free_qty", freeQty,
				)
				pp.orderReq.Qty = truncateQty(h.helmRuntime.filtersFor(ctx, pp.sig.Symbol), freeQty)
				result, err = h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, pp.orderReq)
			}
		}
	}
	// Ambiguous failure (timeout / network): the order may have landed. Ask the exchange
	// by clid before giving up — if it exists, treat the placement as successful.
	if err != nil {
		if recovered := h.recoverAmbiguousPlace(ctx, pp.sig.Symbol, pp.clid, err); recovered != nil {
			result, err = recovered, nil
		}
	}
	pp.result = result
	pp.err = err
}

// applyPlaceResult is the state-mutation phase of an entry/exit: it runs ON the actor loop
// (single-owner) after runPlaceREST returned, recording the order / poslog on success or
// cleaning up tracking + auto-pausing on failure.
func (h *Hand) applyPlaceResult(ctx context.Context, pp *pendingPlace) {
	sig := pp.sig
	intent := pp.intent
	reply := pp.reply
	pending := pp.pending
	clid := pp.clid
	orderType := pp.orderType
	limitPrice := pp.limitPrice
	orderQty := pp.orderQty
	isExitOrder := pp.isExitOrder
	isFutures := pp.isFutures
	signalAt := pp.signalAt
	result := pp.result
	err := pp.err

	if err != nil {
		// Order never reached the exchange — drop the pre-placement clid tracking so it
		// doesn't linger in the routing map.
		h.helmRuntime.RemoveOrderTracking(clid)
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
			"qty", orderQty,
			"order_type", orderType,
			"err", err,
		)
		h.emitEvent(natsapi.HelmEvent{
			Code:      CodeOrderFailed,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Side:      reply.Side,
			Qty:       orderQty,
			Reason:    err.Error(),
			Msg:       "order: placement failed",
		})
		// Auto-pause when a sizing/lot constraint causes a persistent entry failure.
		// Only stop if this hand has no open position — if we already hold a position
		// the failure is on a scale-in or exit, which should not stop the hand.
		// Use h.pos (per-hand) not Portfolio.GetPosition (net helm) so that another
		// hand holding the same symbol does not suppress the auto-stop.
		if isLotSizeError(err) && !isExitOrder {
			h.mu.RLock()
			flat := h.pos.ActiveCount() == 0
			h.mu.RUnlock()
			if flat {
				h.emitEvent(natsapi.HelmEvent{
					Code:   CodeHandAutoStopped,
					Symbol: sig.Symbol,
					Reason: fmt.Sprintf("lot/notional constraint — %s", err.Error()),
					Msg:    "hand: auto-stopped due to sizing constraint",
				})
				go h.Stop()
			}
		}
		// Lot-size error on an EXIT order: qty is below the exchange minimum.
		// Close the poslog position without placing an exchange order — the unsold
		// dust stays in the helm portfolio (not the hand's concern). Prevents an
		// infinite retry loop where checkExits keeps firing for a tiny position
		// that can never be sold.
		if isLotSizeError(err) && isExitOrder && !isFutures {
			slog.Warn("order: exit qty below exchange minimum — dust exit (poslog closed without sell)",
				"hand_id", h.id, "symbol", sig.Symbol, "qty", orderQty, "err", err)
			h.emitEvent(natsapi.HelmEvent{
				Code:   CodeOrderDustExit,
				Symbol: sig.Symbol,
				Qty:    orderQty,
				Reason: fmt.Sprintf("exit qty %s below exchange minimum (%s) — dust_exit", orderQty, err.Error()),
				Msg:    "order: dust exit — position too small for exchange sell",
			})
			h.helmRuntime.RecordDust(sig.Symbol, orderQty)
			lastPrice := h.helmRuntime.lastKnownPrice(sig.Symbol)
			h.closeLegAsDust(ctx, sig.Symbol, reply.Side, orderQty, lastPrice)
		}
		return
	}
	h.metrics.ordersPlaced.Add(1)

	// Use exchange-confirmed base qty for tracking; reply.Qty is zero in quote_qty mode.
	orderedQty := reply.Qty
	if !orderedQty.IsPositive() {
		orderedQty = result.Qty
	}
	// The order is already tracked by its clid (before placement) — the single, race-free
	// routing key. WS fills, REST sync and reconcile all resolve via the clid that the
	// exchange echoes back, so no exchange-id alias is needed. See CLIENT_ORDER_ID.md.

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
	h.publishOrderPlaced(ctx, result.ID, clid, sig.Symbol, reply, limitPrice, orderType, isExitIntent)

	now := time.Now().UTC()
	order := handdomain.Order{
		HandId:        h.id.String(),
		HelmId:        h.helmID.String(),
		ID:            result.ID,
		ClientOrderID: clid,
		Symbol:        sig.Symbol,
		Side:          reply.Side,
		Qty:           orderedQty,
		Type:          string(orderType),
		Status:        result.Status,
		FilledQty:     result.FilledQty,
		FilledAvg:     result.FilledAvg,
		SubmitTime:    now,
	}
	h.mu.Lock()
	h.orders = append(h.orders, order)
	h.health.LastOrderAt = new(now)
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

// computeDefaultSL returns a stop-loss price to use when the signal provides none.
// ATR×5 offset is preferred; falls back to 8% fixed when ATR is zero.
func computeDefaultSL(side string, entryPrice, atr decimal.Decimal) decimal.Decimal {
	if atr.IsPositive() {
		offset := atr.Mul(decimal.NewFromInt(5))
		if side == "buy" {
			return entryPrice.Sub(offset)
		}
		return entryPrice.Add(offset)
	}
	pct := decimal.NewFromFloat(0.08)
	if side == "buy" {
		return entryPrice.Mul(decimal.NewFromInt(1).Sub(pct))
	}
	return entryPrice.Mul(decimal.NewFromInt(1).Add(pct))
}
