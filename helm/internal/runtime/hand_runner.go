package runtime

import (
	"context"
	"fmt"
	"runtime/pprof"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/clid"
	"mallow/helm/internal/runtime/core/strategy"
	"mallow/helm/internal/runtime/position"
	"mallow/helm/internal/safe"
)

// authErrThreshold is the number of consecutive ErrClassAuth responses from PlaceOrder
// before the helm self-pauses and the broker connection is marked error. A value of 3
// tolerates transient 401s (exchange glitch, clock skew) while catching real revocations.
const authErrThreshold = 3

// exitMinFreeFraction is the share of a spot exit's intended qty that must be free
// in the wallet for the exit to proceed. Below it the leg is orphaned, not partially
// sold. 0.99 leaves headroom for the base-asset fee (~0.1%); a genuine shortfall
// (locked co-hand coins, external partial close) is a whole-leg fraction far below
// this, so the two never blur. See design note in runPlaceREST.
var exitMinFreeFraction = decimal.NewFromFloat(0.99)

// Immediate retry budget for a failed EXIT order. The strategy wants out now and the
// exchange OCO has already been cancelled, so we cannot just wait for the next signal.
// Entries never retry (wait for the next signal). Auth / lot-size failures are handled
// separately and are not retried (they won't clear by retrying).
const (
	exitRetryAttempts = 2
	exitRetryDelay    = 400 * time.Millisecond
)

func (h *Hand) run(ctx context.Context) {
	// h.log is pre-tagged with hand_id + helm_id, so a panic identifies its hand.
	defer safe.RecoverHand(h.log)
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
	// bracketPollInterval — how often the REST bracket poller queries exit-order states.
	// WS catches fills in < 1s; polling is a fallback for missed events, so
	// 30s is sufficient and avoids hammering the exchange REST API.
	// Must be ≥ bracketPollGrace (30s) so newly placed brackets are never queried on
	// their first tick (OKX orders-algo propagation lag would return not_found).
	pollTicker := time.NewTicker(30 * time.Second)
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
		case <-h.fillSignal:
			h.drainFills(ctx)
			continue
		default:
		}
		// Second-priority drain: bracket cancels (after fills).
		// Fills must always be processed first so that cancelExitOrders populates
		// pendingCancels before HandleExitOrderCanceled checks it.
		select {
		case orderID := <-h.exitCancelCh:
			h.HandleExitOrderCanceled(ctx, orderID)
			continue
		default:
		}

		select {
		case sig := <-h.Signals:
			h.handleSignal(ctx, sig)
		case <-h.fillSignal:
			h.drainFills(ctx)
		case orderID := <-h.exitCancelCh:
			h.HandleExitOrderCanceled(ctx, orderID)
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
					defer safe.Recover()
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
			h.checkExits() // mostly in-memory; conditional REST already off-loaded
		case <-staleTicker.C:
			h.checkStale()
		case <-ctx.Done():
			return
		}
	}
}

const seenFillsTTL = 2 * time.Minute

func (h *Hand) checkStale() {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Prune seenFills by TTL. Entries older than 2 minutes are safe to drop:
	// Binance WS delivers fill events within ~100ms of the fill — no late event
	// will arrive after 2 minutes. Pruning by TTL (rather than by order status)
	// avoids the race where an order is marked terminal before its WS fill arrives.
	now := time.Now()
	for id, t := range h.seenFills {
		if now.Sub(t) > seenFillsTTL {
			delete(h.seenFills, id)
		}
	}

	// Prune pendingCancels whose orderID no longer appears in any exitLevels entry.
	// These are stale: the WS cancel event was missed (restart, brief disconnect)
	// and the bracket is already gone at the exchange. Keeping them would permanently
	// suppress external-close detection for those IDs.
	if len(h.pendingCancels) > 0 {
		activeIDs := make(map[string]struct{})
		for _, lv := range h.exitLevels {
			for _, id := range lv.ExchangeOrderIDs {
				activeIDs[id] = struct{}{}
			}
		}
		for id := range h.pendingCancels {
			if _, stillActive := activeIDs[id]; !stillActive {
				delete(h.pendingCancels, id)
			}
		}
	}

	// partialApplied is a safety-net prune: entries are normally deleted when the
	// order reaches a terminal state in pollOrders. This handles edge cases where
	// the order was removed without going through the normal terminal path.
	if len(h.partialApplied) > 50 {
		live := make(map[string]struct{}, len(h.orders))
		for _, o := range h.orders {
			switch o.Status {
			case "new", "accepted", "pending_new", "partially_filled", "submitted":
				live[o.ID] = struct{}{}
			}
		}
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
	h.log.Debug("signal: hand received",
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
		h.log.Debug("signal: filtered",
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

	if intent.Action == strategy.ActionEnterShort {
		filtered(CodeSignalRejected, "not support short selling yet")
		return
	}

	h.log.Debug("signal: strategy evaluated",
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

	h.log.Debug("signal: process trade result",
		"symbol", sig.Symbol,
		"approved", reply.Approved,
		"side", reply.Side,
		"qty", reply.Qty,
		"sl", reply.StopLoss,
		"tp", reply.TakeProfit,
		"reason", reply.Reason,
	)

	if !reply.Approved {
		h.log.Warn("signal: trade rejected",
			"symbol", sig.Symbol,
			"action", intent.Action,
			"reason", reply.Reason,
		)
		filtered(CodeSignalRejected, reply.Reason)
		h.mu.RLock()
		activeCount := h.pos.ActiveCount()
		h.mu.RUnlock()
		if activeCount == 0 && reply.Reason == "tactics: zero quantity after sizing" {
			h.log.Warn("hand: no open positions and cannot size entry — auto-stopping",
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
	h.emitEvent(natsapi.HelmEvent{
		Code:      CodeTradeApproved,
		Symbol:    sig.Symbol,
		Direction: string(sig.Direction),
		Msg:       "hand: trade approved",
	})

	// Inject a default SL for entry orders that have no explicit stop loss.
	// Prevents naked entries while still letting the user omit sl in their script.
	// Applied before publishOrderPlaced so the poslog payload captures the injected level.
	if isEntry && reply.StopLoss.IsZero() && sig.StopPrice.IsZero() {
		entryPrice := h.helmRuntime.lastKnownPrice(sig.Symbol)
		if entryPrice.IsPositive() {
			reply.StopLoss = computeDefaultSL(reply.Side, entryPrice, sig.ATR)
			h.log.Info("hand: default SL injected",
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
	}
	// If truncation rounded the qty to zero, the entire position is sub-step dust.
	// Close the poslog leg without placing an exchange order — the tiny remainder
	// stays in the helm-level portfolio (not the hand's concern).
	if isExitOrder && !isFutures && orderQty.IsZero() {
		h.log.Info("order: exit qty rounded to zero — dust exit (no exchange order placed)",
			"symbol", sig.Symbol, "original_qty", reply.Qty)
		h.emitEvent(natsapi.HelmEvent{
			Code:   CodeOrderDustExit,
			Symbol: sig.Symbol,
			Qty:    reply.Qty,
			Reason: fmt.Sprintf("exit qty %s rounds to zero after truncation — dust_exit", reply.Qty),
			Msg:    "order: sub-step dust exit — poslog closed without exchange sell",
		})
		lastPrice := h.helmRuntime.lastKnownPrice(sig.Symbol)
		h.closeLegAsDust(ctx, sig.Symbol, reply.Side, reply.Qty, lastPrice)
		return
	}
	// Generate the client order id and track it BEFORE placing the order, so a WS
	// fill that races ahead of the REST response still routes to this hand via the
	// clid (the exchange does not know it yet, but we already do). See CLIENT_ORDER_ID.md.
	clid := clid.New()
	var marginMode string
	if isFutures && h.cfg.futuresConfig != nil {
		marginMode = string(h.cfg.futuresConfig.MarginType)
	}
	orderReq := exchange.OrderRequest{
		Symbol:        sig.Symbol,
		Side:          exchange.OrderSide(reply.Side),
		Type:          orderType,
		TIF:           exchange.TimeInForce(reply.TIF),
		Qty:           orderQty,
		QuoteQty:      reply.QuoteQty,
		Price:         limitPrice,
		ReduceOnly:    isFutures && isExitOrder,
		IsExit:        isExitOrder,
		MarginMode:    marginMode,
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
		defer safe.Recover()
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
	// positionGoneAtExchange is set by runPlaceREST when a sell exit fails with
	// insufficient balance AND GetFreeBalance returns zero. This means the base
	// asset has been fully sold externally (e.g. OCO triggered concurrently).
	// applyPlaceResult closes the poslog leg via dust_exit as a safety net,
	// in case the orders-algo WS fill event was slow or missed.
	positionGoneAtExchange bool
	// orphanInsufficientFree is set by runPlaceREST when the wallet's free base
	// balance is meaningfully below this hand's share (co-hand coins locked, or an
	// external partial close). REST is skipped; applyPlaceResult disowns the leg
	// (KindPositionOrphaned, no trade record) rather than booking a partial sell.
	orphanInsufficientFree bool
	// ocoFillPrice / ocoFillQty / ocoFillSide carry the actual OCO execution
	// details fetched from the exchange when positionGoneAtExchange=true.
	// Used instead of lastKnownPrice so the trade record reflects the real
	// OCO execution price (not an approximation from the bar-close cache).
	cancelledBracketIDs []string
	ocoFillPrice        decimal.Decimal
	ocoFillQty          decimal.Decimal
	ocoFillSide         string
}

// runPlaceREST is the I/O phase of an entry/exit: PlaceOrder plus the insufficient-balance
// retry and the ambiguous-failure (clid) recovery. Pure REST — it mutates no hand state
// (the clid was tracked on-loop before this runs) — so it executes OFF the actor loop.
func (h *Hand) runPlaceREST(ctx context.Context, pp *pendingPlace) {
	// ── Pre-flight: cancel bracket orders before a signal exit ───────────────
	// Spot OCO bracket orders lock the entire base asset in resting sell orders.
	// If a signal exit arrives while brackets are active, a direct market sell
	// fails because freeBalance(base) == 0 — all qty is locked in the OCO.
	// Fix: cancel the brackets synchronously (in this goroutine, off-loop) so
	// the asset is freed before PlaceOrder, then refresh qty from actual balance.
	//
	// Scope: spot sell exits only. Futures ReduceOnly orders don't lock margin.
	//
	// Race: if the OCO triggers between our cancel and PlaceOrder, cancel returns
	// "order not found" (idempotent error), and the subsequent PlaceOrder fails
	// with insufficient balance — caught by the existing retry below.
	// Futures: still cancel TP/SL brackets before placing the exit order.
	// Unlike spot, futures brackets don't lock assets, so no free balance
	// check or balance-unfreeze wait is required before PlaceOrder.
	if pp.isExitOrder && !pp.isFutures && pp.reply.Side == "sell" {
		h.mu.Lock()
		lv, hasBracket := h.exitLevels[pp.sig.Symbol]
		var bracketIDs []string
		if hasBracket && len(lv.ExchangeOrderIDs) > 0 {
			bracketIDs = append(bracketIDs, lv.ExchangeOrderIDs...)
			// Mark as helm-initiated so HandleExitOrderCanceled ignores the WS
			// "canceled" events that arrive after we cancel.
			// NOTE: ExchangeOrderIDs is intentionally NOT cleared here. Keeping the
			// IDs allows isBracketExit detection in applyFill to work correctly when
			// the OCO fires concurrently with the signal exit (race window between the
			// cancel call and PlaceOrder). Without the IDs, orders-algo fills arrive
			// as orphan fills → poslog not closed → EXT_CLOSE.
			// pendingCancels is sufficient to suppress WS cancel events being
			// misread as external closes — no need to clear IDs.
			for _, id := range bracketIDs {
				h.pendingCancels[id] = struct{}{}
			}
		}
		h.mu.Unlock()

		if len(bracketIDs) > 0 {
			cancelCtx, cancelFn := context.WithTimeout(ctx, 5*time.Second)
			for _, id := range bracketIDs {
				if err := h.helmRuntime.Exchange.CancelOrder(cancelCtx, h.helmRuntime.Creds, id); err != nil {
					h.log.Warn("hand: pre-exit bracket cancel",
						"symbol", pp.sig.Symbol, "order_id", id, "err", err)
				}
			}
			cancelFn()
			h.log.Info("hand: pre-exit bracket cancelled",
				"symbol", pp.sig.Symbol, "cancelled", bracketIDs)
			// Record the cancellation so applyPlaceResult can re-arm the local SL/TP
			// monitor if the exit then fails — the exchange OCO is gone, so leaving the
			// position relying on it would be fatal.
			pp.cancelledBracketIDs = bracketIDs
		}

		// Always check free balance before placing a spot sell exit, regardless of
		// whether brackets were active. Two invariants:
		//   (a) Don't sell more than freeBalance — poslog qty may be gross while the
		//       wallet holds net (fee deducted in base asset), or the position may have
		//       been fully closed externally (OCO triggered while helm was down).
		//   (b) Don't sell more than pp.orderQty — that's this hand's share.
		//       freeBalance includes other hands' capital on the same symbol.
		//
		// When brackets were cancelled: OKX cancel-algos is eventually consistent and
		// the unfreeze can lag (sometimes > 1s). Because the all-or-orphan decision
		// below is IRREVERSIBLE (a wrong call disowns a live position), we must wait
		// for the balance to actually reflect the full share before deciding — break
		// only once free ≥ our share, and back off exponentially (300/600/1200/2400ms,
		// up to 5 attempts ≈ 4.5s) to give a slow OKX time. A shortfall that survives
		// that long is no longer "lag" — it's a genuine shortfall (co-hand coins / an
		// external close), which is exactly what should orphan.
		// When no brackets: one shot is sufficient (nothing to wait for).
		if bf, ok := h.helmRuntime.Exchange.(exchange.SpotBalanceFetcher); ok {
			baseAsset := spotBaseAsset(pp.sig.Symbol)
			maxAttempts := 1
			if len(bracketIDs) > 0 {
				maxAttempts = 5
			}
			enough := pp.orderQty.Mul(exitMinFreeFraction)
			delay := 300 * time.Millisecond
			var freeQty decimal.Decimal
			for attempt := 0; attempt < maxAttempts; attempt++ {
				if attempt > 0 {
					select {
					case <-ctx.Done():
						goto balanceDone
					case <-time.After(delay):
					}
					delay *= 2 // 300 → 600 → 1200 → 2400ms
				}
				balCtx, balFn := context.WithTimeout(ctx, 3*time.Second)
				freeQty, _ = bf.GetFreeBalance(balCtx, h.helmRuntime.Creds, baseAsset)
				balFn()
				if freeQty.GreaterThanOrEqual(enough) {
					break // unfrozen enough to cover our share — stop waiting
				}
			}
		balanceDone:
			if freeQty.IsZero() {
				// Balance is zero after cancel + retry. Two distinct causes:
				//   A. Exchange triggered the OCO concurrently → position really closed
				//   B. OKX balance unfreeze lag (we cancelled the OCO ourselves; the
				//      exchange confirms cancel but takes > 600ms to reflect in balance)
				//
				// Distinguish by querying the OCO order status:
				//   • "filled"    → genuine external close (case A)
				//   • "cancelled" → helm-initiated cancel confirmed; balance just slow
				//                   to unfreeze (case B) → use poslog qty for PlaceOrder
				//   • anything else / error → treat as position gone (safe default)
				for _, id := range bracketIDs {
					r, qErr := h.helmRuntime.Exchange.GetOrder(ctx, h.helmRuntime.Creds, id)
					if qErr != nil || r == nil {
						continue
					}
					if r.Status == "filled" && r.FilledQty.IsPositive() && r.FilledAvg.IsPositive() {
						// Case A: exchange triggered the OCO → recover fill price.
						pp.ocoFillPrice = r.FilledAvg
						pp.ocoFillQty = r.FilledQty
						pp.ocoFillSide = string(r.Side)
						if pp.ocoFillSide == "" {
							pp.ocoFillSide = "sell"
						}
						h.log.Info("hand: OCO fill recovered from exchange (balance=0 after cancel)",
							"symbol", pp.sig.Symbol,
							"order_id", id, "price", r.FilledAvg, "qty", r.FilledQty)
						break
					}
					if r.Status == "canceled" || r.Status == "cancelled" {
						// Case B: helm-initiated cancel confirmed; OKX balance is
						// eventually consistent. Use poslog qty so PlaceOrder can
						// proceed — OKX will have freed the balance by the time the
						// REST call lands (~50-200ms network round-trip).
						h.log.Info("hand: OCO cancel confirmed — OKX balance lag; using poslog qty for exit",
							"symbol", pp.sig.Symbol,
							"order_id", id, "poslog_qty", pp.orderQty)
						freeQty = pp.orderQty
						break
					}
				}
				if freeQty.IsZero() {
					// Balance still zero and no "cancelled" OCO found to explain it.
					// Position is genuinely gone (external close, manual sell, etc.).
					h.log.Warn("hand: pre-exit balance check — base asset gone, skipping PlaceOrder",
						"symbol", pp.sig.Symbol, "poslog_qty", pp.orderQty)
					pp.positionGoneAtExchange = true
					pp.cancelledBracketIDs = bracketIDs
					return
				}
			}
			// All-or-orphan. Only place the exit if the wallet holds (essentially)
			// this hand's full share. The wallet is gross; the leg is gross too, so
			// a small gap is just the base-asset fee (handled later by dust
			// reconciliation). A *meaningful* shortfall means the share isn't really
			// here — another hand's coins on the same symbol are locked, or an
			// external partial close took a chunk. Selling that partial would book a
			// corrupt round-trip trade (e.g. a 50% "loss" that never happened), so we
			// orphan the leg instead: leave it at the exchange, record NO trade.
			if freeQty.LessThan(pp.orderQty.Mul(exitMinFreeFraction)) {
				h.log.Warn("hand: pre-exit free below hand's share — orphaning leg (no partial sell)",
					"symbol", pp.sig.Symbol, "free", freeQty, "share", pp.orderQty)
				pp.orphanInsufficientFree = true
				pp.cancelledBracketIDs = bracketIDs
				return
			}
			useQty := freeQty
			if pp.orderQty.LessThan(freeQty) {
				useQty = pp.orderQty // wallet holds more (other hands) — cap at our share
			}
			useQty = truncateQty(h.helmRuntime.filtersFor(ctx, pp.sig.Symbol), useQty)
			if useQty.IsPositive() {
				pp.orderReq.Qty = useQty
				pp.orderQty = useQty
			}
			h.log.Info("hand: pre-exit balance confirmed",
				"symbol", pp.sig.Symbol, "exit_qty", pp.orderQty, "free_balance", freeQty)
		}
	}
	// Publish KindOrderPlace to poslog as WAL before we make the exchange REST calls.
	h.publishOrderPlace(ctx, pp.clid, pp.sig.Symbol, pp.reply, pp.limitPrice, pp.orderType, pp.isExitOrder)

	result, err := h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, pp.orderReq)
	// Clock skew on exit orders: resync and retry once before falling through to the
	// ambiguous-failure recovery. Entry orders don't retry — wait for the next signal.
	if err != nil && pp.isExitOrder {
		classifier, hasClassifier := h.helmRuntime.Exchange.(exchange.ErrorClassifier)
		if hasClassifier && classifier.ClassifyError(err) == exchange.ErrClassClockSkew {
			h.log.Warn("hand: clock skew on exit PlaceOrder — resyncing and retrying once",
				"symbol", pp.sig.Symbol, "err", err)
			if ts, ok := h.helmRuntime.Exchange.(exchange.TimeSyncer); ok {
				if syncErr := ts.SyncTime(ctx); syncErr != nil {
					h.log.Warn("hand: SyncTime failed during clock-skew recovery", "err", syncErr)
				}
			}
			result, err = h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, pp.orderReq)
		}
	}
	// Ambiguous failure (timeout / network): the order may have landed. Ask the exchange
	// by clid before giving up — if it exists, treat the placement as successful.
	if err != nil {
		if recovered := h.recoverAmbiguousPlace(ctx, pp.sig.Symbol, pp.clid, err); recovered != nil {
			result, err = recovered, nil
		}
	}
	// Immediate bounded retry for a still-failing EXIT (transient network / 5xx).
	// Skip auth (handled via streak) and lot-size (handled via dust exit) — retrying
	// won't clear those. Each attempt re-checks the ambiguous path. On exhaustion the
	// leg stays open and applyPlaceResult re-arms the local SL/TP monitor.
	for attempt := 0; err != nil && pp.isExitOrder && attempt < exitRetryAttempts; attempt++ {
		if exchange.ClassifyGeneric(err) == exchange.ErrClassAuth || isLotSizeError(err) {
			break
		}
		select {
		case <-ctx.Done():
			pp.result, pp.err = result, err
			return
		case <-time.After(exitRetryDelay):
		}
		h.log.Warn("hand: exit PlaceOrder failed — retrying",
			"symbol", pp.sig.Symbol, "attempt", attempt+1, "of", exitRetryAttempts, "err", err)
		result, err = h.helmRuntime.Exchange.PlaceOrder(ctx, h.helmRuntime.Creds, pp.orderReq)
		if err != nil {
			if recovered := h.recoverAmbiguousPlace(ctx, pp.sig.Symbol, pp.clid, err); recovered != nil {
				result, err = recovered, nil
			}
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

	// orphanInsufficientFree: wallet's free base balance was meaningfully short of
	// this hand's share (co-hand coins locked, or external partial close). REST was
	// skipped — disown the leg (no trade record) rather than book a corrupt partial.
	if pp.orphanInsufficientFree {
		h.helmRuntime.RemoveOrderTracking(clid)
		h.orphanLegsForSymbol(ctx, sig.Symbol, "insufficient_free")
		return
	}

	// positionGoneAtExchange: balance check confirmed the base asset is fully gone
	// (OCO triggered externally, manual sell, etc.). REST was skipped so result==nil
	// and err==nil — must be handled before any result dereference.
	if pp.positionGoneAtExchange {
		h.helmRuntime.RemoveOrderTracking(clid)
		if isExitOrder && !isFutures {
			if pp.ocoFillPrice.IsPositive() && pp.ocoFillQty.IsPositive() {
				// Real OCO fill price recovered — apply as a proper fill so the
				// trade record has the correct exit price and PnL.
				closeSide := pp.ocoFillSide
				if closeSide == "" {
					closeSide = reply.Side
				}
				bracketID := ""
				if len(pp.cancelledBracketIDs) > 0 {
					bracketID = pp.cancelledBracketIDs[0]
				}
				h.log.Info("order: OCO fill recovered — applying real fill",
					"symbol", sig.Symbol,
					"order_id", bracketID, "price", pp.ocoFillPrice, "qty", pp.ocoFillQty)
				// Guard seenFills so a late WS delivery of the same order ID doesn't double-apply.
				if bracketID != "" {
					h.mu.Lock()
					if _, already := h.seenFills[bracketID]; already {
						h.mu.Unlock()
						return
					}
					h.seenFills[bracketID] = time.Now()
					h.mu.Unlock()
				}
				h.applyFill(ctx, bracketID, sig.Symbol, closeSide,
					pp.ocoFillQty, pp.ocoFillPrice, decimal.Zero, "bracket_recovered")
			} else {
				// Fill price not recoverable — fall back to dust exit at last known price.
				h.log.Warn("order: exit failed — base asset gone (OCO likely triggered) — dust exit",
					"symbol", sig.Symbol, "qty", orderQty)
				h.emitEvent(natsapi.HelmEvent{
					Code:   CodeOrderDustExit,
					Symbol: sig.Symbol,
					Qty:    orderQty,
					Reason: "position gone at exchange — OCO likely triggered concurrently",
					Msg:    "order: dust exit — base asset unavailable",
				})
				lastPrice := h.helmRuntime.lastKnownPrice(sig.Symbol)
				h.closeLegAsDust(ctx, sig.Symbol, reply.Side, orderQty, lastPrice)
			}
		}
		return
	}

	if err != nil {
		// Order never reached the exchange — drop the pre-placement clid tracking so it
		// doesn't linger in the routing map.
		h.helmRuntime.RemoveOrderTracking(clid)
		h.metrics.ordersFailed.Add(1)
		h.mu.Lock()
		h.health.LastErrorAt = new(time.Now().UTC())
		h.health.LastError = err.Error()
		h.health.Status = HealthError
		h.mu.Unlock()
		h.log.Error("order: placement failed",
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

		// Revert the in-memory pending state machine transition by publishing order_cancelled
		positionID := clid // opening entry fallback
		h.mu.RLock()
		if leg := h.pos.PrimaryLeg(); leg != nil {
			positionID = leg.PositionID
		}
		h.mu.RUnlock()
		if publishErr := h.publishOrderCancelled(ctx, clid, positionID, err.Error()); publishErr != nil {
			h.log.Error("hand: failed to publish order_cancelled after placement failure", "err", publishErr)
		}
		// Auth error mid-run: credentials were revoked or expired at the exchange.
		// Use a streak counter so a single transient 401 (clock skew, exchange glitch) does
		// not trigger a full pause. Only after authErrThreshold consecutive auth failures do
		// we self-pause the helm and mark the broker connection as error.
		if exchange.ClassifyGeneric(err) == exchange.ErrClassAuth {
			streak := h.helmRuntime.authErrStreak.Add(1)
			if streak >= authErrThreshold {
				h.helmRuntime.authErrStreak.Store(0) // reset so re-entry after resume starts fresh
				h.helmRuntime.TriggerAuthError(
					fmt.Sprintf("exchange rejected credentials (%d consecutive auth failures): %s", streak, err.Error()),
				)
			}
			return
		}
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
			h.log.Warn("order: exit qty below exchange minimum — dust exit (poslog closed without sell)",
				"symbol", sig.Symbol, "qty", orderQty, "err", err)
			h.emitEvent(natsapi.HelmEvent{
				Code:   CodeOrderDustExit,
				Symbol: sig.Symbol,
				Qty:    orderQty,
				Reason: fmt.Sprintf("exit qty %s below exchange minimum (%s) — dust_exit", orderQty, err.Error()),
				Msg:    "order: dust exit — position too small for exchange sell",
			})
			lastPrice := h.helmRuntime.lastKnownPrice(sig.Symbol)
			h.closeLegAsDust(ctx, sig.Symbol, reply.Side, orderQty, lastPrice)
			return
		}

		// Generic exit failure after retries: the leg is still open (publishOrderPlaced
		// was never reached) but its exchange OCO was cancelled in pre-flight — leaving
		// the position relying on a dead bracket would be fatal. Re-arm the IN-PROCESS
		// SL/TP monitor by clearing the stale exchange order IDs so checkExits resumes
		// triggering on price crosses (it skips while ExchangeOrderIDs is non-empty).
		if isExitOrder && !isFutures && len(pp.cancelledBracketIDs) > 0 {
			h.rearmLocalExit(sig.Symbol)
		}
		return
	}
	h.metrics.ordersPlaced.Add(1)
	// Successful placement — credentials are valid; reset the auth error streak.
	h.helmRuntime.authErrStreak.Store(0)

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
	h.log.Info("order: placed",
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

	// ── Position pre-fill lifecycle events ───────────────────────────────────
	// Emitted after CodeOrderPlaced so the order event (low-level placement fact)
	// precedes the position event (higher-level intent) in the activity feed.
	// Skipped for exit orders — those have their own post-fill lifecycle.
	if !isExitOrder {
		h.mu.RLock()
		posID := h.pendingOrderPos[result.ID]
		phase := h.pos.LegPhase(posID)
		var preQty, preAvg decimal.Decimal
		if phase == position.PhaseAdding {
			if snap, ok := h.pos.LegSnapshot(posID); ok {
				preQty = snap.Qty.Abs()
				preAvg = snap.EntryPrice
			}
		}
		h.mu.RUnlock()

		switch phase {
		case position.PhaseEntering:
			h.emitEvent(natsapi.HelmEvent{
				Code:       CodePositionEntering,
				Symbol:     sig.Symbol,
				Side:       reply.Side,
				Qty:        orderedQty,
				Price:      limitPrice,
				PositionID: posID,
				OrderID:    order.ID,
				Msg:        "position: entering",
			})
		case position.PhaseAdding:
			// Show current position state (before this add) so users can see
			// what they have now vs what the add is targeting.
			h.emitEvent(natsapi.HelmEvent{
				Code:       CodePositionAdding,
				Symbol:     sig.Symbol,
				Side:       reply.Side,
				Qty:        orderedQty, // this add's order qty
				Price:      limitPrice,
				PositionID: posID,
				OrderID:    order.ID,
				EntryPrice: preAvg, // current avg BEFORE this add
				Reason:     fmt.Sprintf("current_qty=%s current_avg=%s", preQty, preAvg),
				Msg:        "position: adding (pyramid)",
			})
		}
	}

	// WS-before-ACK recovery: if a WS fill arrived before pendingOrderPos/pendingExits
	// were set, applyFill cached the fill data in wsFillCache. Use that data (not the
	// REST ack, which returns status="submitted" with qty=0 after the ACK refactor) to
	// complete the poslog transition and bracket placement without re-touching the portfolio.
	h.mu.Lock()
	cached, hasCached := h.wsFillCache[result.ID]
	if hasCached {
		delete(h.wsFillCache, result.ID)
	}
	h.mu.Unlock()
	if hasCached {
		h.completeWsFillFromREST(ctx, result.ID, sig.Symbol, reply.Side,
			cached.qty, cached.price, cached.commission, pending, isExitOrder)
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
