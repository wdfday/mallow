package signalfollower

import (
	"context"
	"fmt"
	"mallow/helm/internal/fleet/actor/eventcode"
	"runtime/pprof"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/clid"
	"mallow/helm/internal/fleet/actor/core/strategy"
	"mallow/helm/internal/fleet/actor/position"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/safe"
)

// exitMinFreeFraction is the share of a spot exit's intended qty that must be free
// in the wallet for the exit to proceed. Below it the leg is orphaned, not partially
// sold. NOT headroom for the base-asset fee — NormalizeCommission already nets that
// out of leg.Qty when the fee is paid in the base asset (helm_market.go), and leaves
// qty untouched when it's paid in quote/BNB, so poslog qty already tracks close to the
// true wallet share either way. 0.99 is a buffer for what NormalizeCommission does NOT
// model: rounding drift across many partial fills, exchange-specific fee quirks (see
// the OKX base-asset fee deduction note in applyPolledOrders), and order-placement vs.
// exit-time truncation mismatches. A genuine shortfall (locked co-hand coins, external
// partial close) is a whole-leg fraction far below this, so the two never blur — do not
// tighten toward 1.0 without evidence that residual noise stays under it on every
// exchange; orphaning is irreversible, so slack here is cheaper than a false positive.
// See design note in runPlaceREST.
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
		case plt := <-h.limitTimeoutCh:
			// Off-loop limit-timeout cancel/fallback came back — poslog + tracking on-loop.
			h.applyLimitTimeoutResult(ctx, plt)
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
	// Orphan detection isn't a market signal — it shouldn't be TTL-expired,
	// rate-limited, or interpreted by h.strategy.Evaluate. Intercept before any
	// of that and hand off to the dedicated orphan path.
	if sig.ExitKind == strategy.ExitKindOrphan {
		h.handleOrphanSignal(ctx, sig)
		return
	}

	// Signal audit log: herald-originated signals only (empty ExitKind = entry,
	// ExitKindSignal = herald exit). checkExits' local TP/SL triggers are
	// helm's own reactive machinery, not something herald told us — excluded
	// so the audit trail reflects the strategy's real inputs. ExitKindOrphan
	// never reaches here (already returned above).
	if sig.ExitKind != strategy.ExitKindTakeProfit && sig.ExitKind != strategy.ExitKindStopLoss {
		h.helm.PublishSignal(natsapi.SignalMsg{
			HelmID:      h.helmID.String(),
			HandID:      h.id.String(),
			UserID:      h.helm.GetUserID().String(),
			Symbol:      sig.Symbol,
			Direction:   string(sig.Direction),
			ExitKind:    string(sig.ExitKind),
			Strength:    sig.Strength,
			Price:       sig.Price.String(),
			TargetPrice: sig.TargetPrice.String(),
			StopPrice:   sig.StopPrice.String(),
			IsOffset:    sig.IsOffset,
			ATR:         sig.ATR.String(),
			Reason:      sig.Reason,
			GeneratedAt: sig.GeneratedAt,
			ReceivedAt:  sig.ReceivedAt,
		})
	}

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

	h.EmitEvent(natsapi.HelmEvent{
		Code:      eventcode.CodeSignalReceived,
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
		h.EmitEvent(natsapi.HelmEvent{
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
		filtered(eventcode.CodeSignalStale, reason)
		return
	}

	h.mu.Lock()
	h.health.LastSignalAt = new(time.Now().UTC())
	h.mu.Unlock()

	if h.helm.IsPaused() {
		filtered(eventcode.CodeSignalHelmPaused, "helm paused")
		return
	}
	if !h.limiter.Allow() {
		filtered(eventcode.CodeSignalRateLimited, "rate limited")
		return
	}

	intent := h.strategy.Evaluate(sig)

	if intent.Action == strategy.ActionEnterShort {
		filtered(eventcode.CodeSignalRejected, "not support short selling yet")
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
		filtered(eventcode.CodeSignalDoNothing, reason)
		return
	}

	// exitTradeID is the specific leg this exit targets, threaded through
	// pendingPlace to runPlaceREST/rearmLocalExit — both need it to key
	// h.exitLevels (which is keyed by TradeID, not symbol, so that independent
	// same-symbol legs each keep their own bracket tracking).
	var exitTradeID string
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
		//
		// sig.PositionID (set by checkExits for a locally-triggered exit) pins the
		// exact leg that tripped SL/TP — required when multiple independent legs
		// share a symbol (non-pyramid, MaxUnits>1), since "first match" would
		// otherwise pick an arbitrary leg. Herald/NATS-originated exit signals
		// carry no PositionID (herald has no concept of legs) and fall back to
		// the existing first-match-by-symbol convention.
		h.mu.RLock()
		var handSide string
		for _, leg := range h.pos.ActiveLegs() {
			if leg.Phase != position.PhaseOpen && leg.Phase != position.PhaseAdding {
				continue
			}
			if sig.PositionID != "" {
				if leg.TradeID == sig.PositionID {
					handSide = leg.Side
					exitTradeID = leg.TradeID
					break
				}
				continue
			}
			if leg.Symbol == sig.Symbol {
				handSide = leg.Side
				exitTradeID = leg.TradeID
				break
			}
		}
		h.mu.RUnlock()
		if handSide == "" {
			filtered(eventcode.CodeSignalNoPosition, "exit signal: position already closed")
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
			filtered(eventcode.CodeSignalMaxUnits, fmt.Sprintf("max units reached (%d/%d)", count, h.cfg.maxUnits))
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
			px := h.helm.LastKnownPrice(sig.Symbol)
			winning := px.IsPositive() &&
				((legSide == "buy" && px.GreaterThan(legAvg)) ||
					(legSide == "sell" && px.LessThan(legAvg)))
			if !winning {
				filtered(eventcode.CodeSignalDoNothing, fmt.Sprintf(
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
		equityOverride = h.RealizedEquity()
		availableBudget = h.AvailableCash()
	}

	reply := h.helm.ProcessTrade(ctx, TradeProposal{
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
		filtered(eventcode.CodeSignalRejected, reply.Reason)
		h.mu.RLock()
		activeCount := h.pos.ActiveCount()
		h.mu.RUnlock()
		if activeCount == 0 && reply.Reason == "tactics: zero quantity after sizing" {
			h.log.Warn("hand: no open positions and cannot size entry — auto-stopping",
				"symbol", sig.Symbol,
				"reason", reply.Reason,
			)
			h.EmitEvent(natsapi.HelmEvent{
				Code:   eventcode.CodeHandAutoStopped,
				Symbol: sig.Symbol,
				Reason: reply.Reason,
				Msg:    "hand: auto-stopped — cannot size entry with zero open positions",
			})
			go h.Stop()
		}
		return
	}
	h.metrics.tradesApproved.Add(1)
	h.EmitEvent(natsapi.HelmEvent{
		Code:      eventcode.CodeTradeApproved,
		Symbol:    sig.Symbol,
		Direction: string(sig.Direction),
		Msg:       "hand: trade approved",
	})

	// Inject a default SL for entry orders that have no explicit stop loss.
	// Prevents naked entries while still letting the user omit sl in their script.
	// Applied before publishOrderPlaced so the poslog payload captures the injected level.
	if isEntry && reply.StopLoss.IsZero() && sig.StopPrice.IsZero() {
		entryPrice := h.helm.LastKnownPrice(sig.Symbol)
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

	isFutures := h.cfg.isFutures
	isExitOrder := intent.Action == strategy.ActionExitLong || intent.Action == strategy.ActionExitShort

	// Apply leverage/margin type on first entry per symbol for futures hands.
	// EnsureLeverage is actor-owned (helm_actor.go) — no-ops if already set for
	// this symbol on this helm's account, correctly coordinating across hands
	// that might otherwise race on the exchange's single account+symbol setting.
	if isFutures && !isExitOrder {
		h.helm.EnsureLeverage(ctx, sig.Symbol, h.cfg.futuresConfig)
	}

	orderQty := reply.Qty
	// Truncate qty to the exchange's LOT_SIZE stepSize so the order is a valid
	// multiple of the step. Futures use ReduceOnly and have their own precision.
	if !isFutures {
		orderQty = TruncateQty(h.helm.FiltersFor(ctx, sig.Symbol), orderQty)
	}
	// If truncation rounded the qty to zero, the entire position is sub-step dust.
	// Close the poslog leg without placing an exchange order — the tiny remainder
	// stays in the helm-level portfolio (not the hand's concern).
	if isExitOrder && !isFutures && orderQty.IsZero() {
		h.log.Info("order: exit qty rounded to zero — dust exit (no exchange order placed)",
			"symbol", sig.Symbol, "original_qty", reply.Qty)
		h.EmitEvent(natsapi.HelmEvent{
			Code:   eventcode.CodeOrderDustExit,
			Symbol: sig.Symbol,
			Qty:    reply.Qty,
			Reason: fmt.Sprintf("exit qty %s rounds to zero after truncation — dust_exit", reply.Qty),
			Msg:    "order: sub-step dust exit — poslog closed without exchange sell",
		})
		lastPrice := h.helm.LastKnownPrice(sig.Symbol)
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
		exitTradeID: exitTradeID,
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
	// exitTradeID is the specific leg being exited (empty for entries) — resolved
	// in handleSignal's sig.IsUrgent() branch. h.exitLevels is keyed by TradeID,
	// not symbol, so runPlaceREST/rearmLocalExit need this to find the right entry.
	exitTradeID string

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
	// Used instead of LastKnownPrice so the trade record reflects the real
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
		lv, hasBracket := h.exitLevels[pp.exitTradeID]
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
				if err := h.helm.GetExchange().CancelOrder(cancelCtx, h.helm.GetCreds(), id); err != nil {
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
		if bf, ok := h.helm.GetExchange().(exchange.SpotBalanceFetcher); ok {
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
				freeQty, _ = bf.GetFreeBalance(balCtx, h.helm.GetCreds(), baseAsset)
				balFn()
				if freeQty.GreaterThanOrEqual(enough) {
					break // unfrozen enough to cover our share — stop waiting
				}
			}
		balanceDone:
			if freeQty.LessThan(enough) {
				// Balance is short of our share after cancel + retry — not just the
				// exactly-zero case, any shortfall (including a partial one from a
				// different hand's lock on the same wallet) gets the same chance at an
				// explanation below, rather than skipping straight to orphan. The retry
				// loop above already spent up to ~4.5s waiting out ordinary OKX unfreeze
				// lag (its own comment: a shortfall surviving that long is no longer
				// "lag"), so reaching here means one of two things, not "maybe still lag":
				//   A. Exchange triggered the OCO concurrently → position really closed
				//   B. Our own cancel is independently confirmed done (GetOrder says
				//      "cancelled") but the wallet still hasn't caught up — unusually
				//      slow even by OKX standards, but the cancel itself is a known fact,
				//      not a guess, so it's still safe to proceed on poslog qty
				//
				// Distinguish by querying the OCO order status:
				//   • "filled"    → genuine external close (case A)
				//   • "cancelled" → our cancel confirmed done independently of the
				//                   balance reading (case B) → use poslog qty for PlaceOrder
				//   • anything else / error → inconclusive; falls through to the
				//     all-or-orphan check below like any other unexplained shortfall
				caseAMatched := false
				for _, id := range bracketIDs {
					r, qErr := h.helm.GetExchange().GetOrder(ctx, h.helm.GetCreds(), id)
					if qErr != nil || r == nil {
						continue
					}
					if r.Status == "filled" && r.FilledQty.IsPositive() && r.FilledAvg.IsPositive() {
						// Case A: exchange triggered the OCO → recover fill price. Does NOT
						// touch freeQty — the recovery lives entirely in the ocoFill* fields,
						// so the outcome below must key off caseAMatched, not freeQty being
						// zero (freeQty can be a nonzero-but-insufficient shortfall here now
						// that the outer gate is LessThan(enough) instead of IsZero()).
						pp.ocoFillPrice = r.FilledAvg
						pp.ocoFillQty = r.FilledQty
						pp.ocoFillSide = string(r.Side)
						if pp.ocoFillSide == "" {
							pp.ocoFillSide = "sell"
						}
						caseAMatched = true
						h.log.Info("hand: OCO fill recovered from exchange (balance short after cancel)",
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
				if caseAMatched || freeQty.IsZero() {
					// Either case A resolved it directly, or balance is still exactly zero
					// with no "cancelled" OCO found to explain it either — position is
					// genuinely gone (external close, manual sell, etc.).
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
			useQty = TruncateQty(h.helm.FiltersFor(ctx, pp.sig.Symbol), useQty)
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

	result, err := h.helm.GetExchange().PlaceOrder(ctx, h.helm.GetCreds(), pp.orderReq)
	// Clock skew on exit orders: resync and retry once before falling through to the
	// ambiguous-failure recovery. Entry orders don't retry — wait for the next signal.
	if err != nil && pp.isExitOrder {
		if ClassifyErr(h.helm.GetExchange(), err) == exchange.ErrClassClockSkew {
			h.log.Warn("hand: clock skew on exit PlaceOrder — resyncing and retrying once",
				"symbol", pp.sig.Symbol, "err", err)
			if ts, ok := h.helm.GetExchange().(exchange.TimeSyncer); ok {
				if syncErr := ts.SyncTime(ctx); syncErr != nil {
					h.log.Warn("hand: SyncTime failed during clock-skew recovery", "err", syncErr)
				}
			}
			result, err = h.helm.GetExchange().PlaceOrder(ctx, h.helm.GetCreds(), pp.orderReq)
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
		if ClassifyErr(h.helm.GetExchange(), err) == exchange.ErrClassAuth || isLotSizeError(h.helm.GetExchange(), err) {
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
		result, err = h.helm.GetExchange().PlaceOrder(ctx, h.helm.GetCreds(), pp.orderReq)
		if err != nil {
			if recovered := h.recoverAmbiguousPlace(ctx, pp.sig.Symbol, pp.clid, err); recovered != nil {
				result, err = recovered, nil
			}
		}
	}
	pp.result = result
	pp.err = err
}

// wsFillCacheRetries × wsFillCacheRetryInterval bounds the WS-before-ACK recovery
// wait in applyPlaceResult above — 5 × 20ms = 100ms, enough to cover the network
// jitter that occasionally lands WS a few ms after the REST response instead of
// before it, without meaningfully delaying this hand's run loop.
const (
	wsFillCacheRetries       = 5
	wsFillCacheRetryInterval = 20 * time.Millisecond
)

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
		h.helm.RemoveOrderTracking(clid)
		h.orphanLegsForSymbol(ctx, sig.Symbol, "insufficient_free")
		return
	}

	// positionGoneAtExchange: balance check confirmed the base asset is fully gone
	// (OCO triggered externally, manual sell, etc.). REST was skipped so result==nil
	// and err==nil — must be handled before any result dereference.
	if pp.positionGoneAtExchange {
		h.helm.RemoveOrderTracking(clid)
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
				// Fill price not recoverable — we don't actually know how (or whether)
				// this position closed, only that the exchange balance says it's gone.
				// closeLegAsDust would fabricate a PnL from LastKnownPrice (never a real
				// fill price) and book it as a genuine trade via appendTradeRecord —
				// exactly the pattern db3ed18 removed from handlePositionDesync for the
				// same "external close suspected, no real price" situation. Use the
				// orphan path instead: no claimed PnL, tagged for audit, consistent with
				// orphanLegsForSymbol / HandleExitOrderCanceled / handlePositionDesync.
				h.log.Warn("order: exit failed — base asset gone (OCO likely triggered), no fill price recovered — orphaning leg",
					"symbol", sig.Symbol, "qty", orderQty)
				h.orphanLegsForSymbol(ctx, sig.Symbol, "position_gone_no_fill_price")
			}
		}
		return
	}

	if err != nil {
		// Order never reached the exchange — drop the pre-placement clid tracking so it
		// doesn't linger in the routing map.
		h.helm.RemoveOrderTracking(clid)
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
		h.EmitEvent(natsapi.HelmEvent{
			Code:      eventcode.CodeOrderFailed,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Side:      reply.Side,
			Qty:       orderQty,
			Reason:    err.Error(),
			Msg:       "order: placement failed",
		})

		// Revert the in-memory pending state machine transition by publishing order_cancelled
		tradeID := clid // opening entry fallback
		h.mu.RLock()
		if leg := h.pos.PrimaryLeg(); leg != nil {
			tradeID = leg.TradeID
		}
		h.mu.RUnlock()
		if publishErr := h.publishOrderCancelled(ctx, clid, tradeID, err.Error()); publishErr != nil {
			h.log.Error("hand: failed to publish order_cancelled after placement failure", "err", publishErr)
		}
		// Account-level error escalation: auth rejection, or a sustained network/
		// exchange-server error. NoteOrderError uses a per-class streak counter
		// (errClassThresholds) so a single transient blip does not trigger a
		// full pause — only classes with a nonzero threshold escalate at all.
		if class := ClassifyErr(h.helm.GetExchange(), err); ErrClassThresholds[class] > 0 {
			h.helm.NoteOrderError(err)
			return
		}
		// Auto-pause when a sizing/lot constraint causes a persistent entry failure.
		// Only stop if this hand has no open position — if we already hold a position
		// the failure is on a scale-in or exit, which should not stop the hand.
		// Use h.pos (per-hand) not Portfolio.GetPosition (net helm) so that another
		// hand holding the same symbol does not suppress the auto-stop.
		if isLotSizeError(h.helm.GetExchange(), err) && !isExitOrder {
			h.mu.RLock()
			flat := h.pos.ActiveCount() == 0
			h.mu.RUnlock()
			if flat {
				h.EmitEvent(natsapi.HelmEvent{
					Code:   eventcode.CodeHandAutoStopped,
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
		if isLotSizeError(h.helm.GetExchange(), err) && isExitOrder && !isFutures {
			h.log.Warn("order: exit qty below exchange minimum — dust exit (poslog closed without sell)",
				"symbol", sig.Symbol, "qty", orderQty, "err", err)
			h.EmitEvent(natsapi.HelmEvent{
				Code:   eventcode.CodeOrderDustExit,
				Symbol: sig.Symbol,
				Qty:    orderQty,
				Reason: fmt.Sprintf("exit qty %s below exchange minimum (%s) — dust_exit", orderQty, err.Error()),
				Msg:    "order: dust exit — position too small for exchange sell",
			})
			lastPrice := h.helm.LastKnownPrice(sig.Symbol)
			h.closeLegAsDust(ctx, sig.Symbol, reply.Side, orderQty, lastPrice)
			return
		}

		// Generic exit failure after retries: the leg is still open (publishOrderPlaced
		// was never reached) but its exchange OCO was cancelled in pre-flight — leaving
		// the position relying on a dead bracket would be fatal. Re-arm the IN-PROCESS
		// SL/TP monitor by clearing the stale exchange order IDs so checkExits resumes
		// triggering on price crosses (it skips while ExchangeOrderIDs is non-empty).
		if isExitOrder && !isFutures && len(pp.cancelledBracketIDs) > 0 {
			h.rearmLocalExit(pp.exitTradeID, sig.Symbol)
		}
		return
	}
	h.metrics.ordersPlaced.Add(1)
	// Successful placement — connection/credentials are fine; reset every class's streak.
	h.helm.ResetErrStreaks()

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
	h.EmitEvent(natsapi.HelmEvent{
		Code:    eventcode.CodeOrderPlaced,
		Symbol:  sig.Symbol,
		Side:    reply.Side,
		Qty:     orderedQty,
		Price:   limitPrice,
		OrderID: order.ID,
		Reason:  placedReason,
		Msg:     "order: placed",
	})

	// ── Position pre-fill lifecycle events ───────────────────────────────────
	// Emitted after eventcode.CodeOrderPlaced so the order event (low-level placement fact)
	// precedes the position event (higher-level intent) in the activity feed.
	// Skipped for exit orders — those have their own post-fill lifecycle.
	if !isExitOrder {
		h.mu.RLock()
		tradeID := h.pendingOrderPos[result.ID]
		phase := h.pos.LegPhase(tradeID)
		var preQty, preAvg decimal.Decimal
		if phase == position.PhaseAdding {
			if snap, ok := h.pos.LegSnapshot(tradeID); ok {
				preQty = snap.Qty.Abs()
				preAvg = snap.EntryPrice
			}
		}
		h.mu.RUnlock()

		switch phase {
		case position.PhaseEntering:
			h.EmitEvent(natsapi.HelmEvent{
				Code:       eventcode.CodePositionEntering,
				Symbol:     sig.Symbol,
				Side:       reply.Side,
				Qty:        orderedQty,
				Price:      limitPrice,
				PositionID: tradeID,
				OrderID:    order.ID,
				Msg:        "position: entering",
			})
		case position.PhaseAdding:
			// Show current position state (before this add) so users can see
			// what they have now vs what the add is targeting.
			h.EmitEvent(natsapi.HelmEvent{
				Code:       eventcode.CodePositionAdding,
				Symbol:     sig.Symbol,
				Side:       reply.Side,
				Qty:        orderedQty, // this add's order qty
				Price:      limitPrice,
				PositionID: tradeID,
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
	//
	// Must run here (on-loop, after pendingOrderPos/pendingExits are set up above by
	// this same function) — NOT earlier in runPlaceREST: completeWsFillFromREST reads
	// h.pendingOrderPos[orderID] itself, and that map is only populated by this
	// function's own bookkeeping, not by runPlaceREST. A retry loop placed in
	// runPlaceREST would therefore always see it empty, no matter how long it waited.
	//
	// Retried a few times over a short bounded window instead of checked once: WS
	// delivery doesn't always land before the REST response by much, and a same-tick
	// miss here previously fell all the way through to the next 30s poll cycle to
	// catch it (observed live: bracket placement stalled ~30s after a fill that had,
	// in fact, already landed a few ms too late for a one-shot check). The retry
	// blocks this hand's own run loop briefly — bounded to wsFillCacheRetries ×
	// wsFillCacheRetryInterval — which is an acceptable, deliberate trade against a
	// 30s stall in bracket protection.
	var cached cachedWsFill
	var hasCached bool
	for i := 0; i < wsFillCacheRetries; i++ {
		h.mu.Lock()
		cached, hasCached = h.wsFillCache[result.ID]
		if hasCached {
			delete(h.wsFillCache, result.ID)
		}
		h.mu.Unlock()
		if hasCached {
			break
		}
		if i < wsFillCacheRetries-1 {
			time.Sleep(wsFillCacheRetryInterval)
		}
	}
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

// KNOWN GAP (2026-07-09, not yet fixed): a futures SL/TP bracket order can reduce the
// WRONG hand's share of a shared exchange position.
//
// scheduleBracketPlacement (hand_fills.go:650) sizes a futures bracket's Qty from THIS
// hand's own poslog leg (h.pos.PrimaryLeg().Qty) at the moment the bracket is placed —
// see hand_fills.go:680-684. That qty is baked into the exchange order as a fixed
// quantity, ReduceOnly, NOT closePosition=true (see fbinance/act/orders.go
// PlaceExitOrders), and is never revisited before the bracket actually triggers, which
// can be minutes or hours later.
//
// The exchange has no concept of "hands." A futures position is netted per symbol per
// account — one pooled number, not N per-hand silos. A ReduceOnly order just subtracts
// its fixed Qty from whatever the pool holds at trigger time; it does not verify that
// the qty it removes is still "this hand's own" contribution.
//
// Failure mode: two hands trade the same futures symbol under the same helm. Hand A
// places a bracket sized for its qty at T0. Between T0 and the bracket's trigger, Hand
// A's own signal exit fires (which cancels ITS bracket via applyExitFill ->
// cancelExitOrders — but only once the exit fill is confirmed; the bracket is live and
// uncancelled for the whole placement-to-confirmation window, see the discussion in
// runPlaceREST's pre-flight cancel comment) and/or Hand B adds to its own position on
// the same symbol in that window. When Hand A's stale bracket then triggers, it reduces
// the CURRENT pooled position by Hand A's OLD qty — which may now belong, in whole or
// in part, to Hand B. Hand B's poslog never learns this: the fill routes back to Hand A
// only (orders are tracked per-owner via TrackOrder), so Hand B's in-memory leg quietly
// diverges from the real exchange position until something notices the mismatch (a
// reconcile pass, or never, if the drift is small enough to pass unnoticed).
//
// Distinct from the multi-hand-same-symbol gap already tracked for spot (net portfolio
// qty vs h.pos in hand_signal.go; wallet-level all-or-orphan in runPlaceREST) — that one
// is decision-level (a check reads the wrong number). This one is execution-level: the
// exchange order itself, once placed, has no way to re-check "is this qty still mine"
// at trigger time — no decision in Go code is even wrong, the order type itself can't
// express the right constraint.
//
// Two fix directions discussed, neither implemented:
//  1. Re-verify remaining attributable qty at trigger time — requires per-hand exchange
//     position attribution, which doesn't exist (futures has none by nature; this would
//     mean helm keeping its own shadow ledger of "whose contracts are these" and
//     reconciling it against the pooled exchange position on every fill).
//  2. Move brackets from per-hand to per-(helm,symbol): one shared bracket tracking net
//     qty across all hands on that symbol, re-sized on every entry/exit by any hand.
//     Bigger change — bracket ownership/cancellation (cancelExitOrders, exitLevels) is
//     currently keyed and reasoned about per-hand throughout this file and hand_fills.go.
//
// Only matters when >1 hand under the same helm trades the same futures symbol
// concurrently. Spot does not have this exact failure mode — no ReduceOnly pooling;
// the spot bracket cancel is pre-flight and balance-gated (see runPlaceREST), so a spot
// exit can never even attempt to place its market order while a stale bracket is still
// live enough to race it.

// KNOWN GAP (2026-07-09, not yet fixed): a failed pre-exit bracket CancelOrder can be
// misread as "position already gone" and cause a live spot position to be dropped from
// poslog while it is still open and protected at the exchange.
//
// In the pre-flight cancel loop (runPlaceREST, spot sell-exit branch), a CancelOrder
// failure per bracket ID is only logged, never surfaced as a distinct outcome:
//
//	for _, id := range bracketIDs {
//		if err := h.helm.GetExchange().CancelOrder(cancelCtx, h.helm.GetCreds(), id); err != nil {
//			h.log.Warn("hand: pre-exit bracket cancel", "symbol", pp.sig.Symbol, "order_id", id, "err", err)
//		}
//	}
//	...
//	pp.cancelledBracketIDs = bracketIDs // set unconditionally — "attempted" is treated as "cancelled"
//
// If the cancel genuinely failed (network blip, exchange timeout), the bracket is still
// live and still holds pp.orderQty locked at the exchange — nothing about that order
// changed. The balance-check loop that follows sees freeQty == 0 (same as it would if
// the cancel HAD succeeded but OKX's unfreeze just hadn't propagated yet — the two are
// indistinguishable from freeQty alone, see the IsZero() note further up this file), so
// it falls into the ambiguity-resolving diagnostic: query the bracket's own order status
// via GetOrder and classify:
//
//	"filled"            -> case A, exchange triggered the OCO for real
//	"cancelled"/"canceled" -> case B, our cancel is confirmed, balance is just slow
//	anything else / error  -> "safe default": treat as position gone
//
// A bracket that failed to cancel is still resting — its GetOrder status is "new" or
// equivalent, which is neither "filled" nor "cancelled". It falls into the third,
// "safe default" branch, which is only safe under the assumption that reaching this
// diagnostic at all already implies the position is gone. That assumption breaks here:
// the position is very much still open, guarded by the bracket that never got cancelled.
//
// Consequence: pp.positionGoneAtExchange is set to true with no recovered OCO fill price
// (ocoFillPrice stays zero, since the status wasn't "filled"). applyPlaceResult then
// takes the no-fill-price branch and calls closeLegAsDust at LastKnownPrice — closing the
// poslog leg as if the position were sold, with no exchange order ever placed and no real
// exit. The exchange still holds the position, still guarded by the (never-cancelled)
// bracket. helm now believes the hand is flat while it is not: the next signal on this
// symbol sees NO_POS and can re-enter as if starting fresh, on top of a position it no
// longer knows exists.
//
// Not fixed. Tracking per-ID CancelOrder success is NOT the fix — if CancelOrder itself
// fails on a network error, whether the cancel actually reached the exchange is exactly
// as unknowable as a PlaceOrder ambiguous failure (see recoverAmbiguousPlace); there is
// no equivalent recovery path for cancels today. The real fix is narrower: the GetOrder
// diagnostic loop right below already distinguishes two truly different outcomes and
// wrongly treats them the same —
//   qErr != nil || r == nil        -> genuinely unknown (the query itself failed)
//   r.Status is neither filled nor
//     cancelled, but the query SUCCEEDED -> definitively still resting, NOT ambiguous
// Only the first is real ambiguity. The second is a confident "still open" answer that
// currently falls through into the same "treat as gone" bucket as the first. Give it its
// own branch (bracket confirmed still live -> do not dust-exit; retry the cancel or leave
// the leg alone and let the bracket keep protecting it) and the failure mode above closes
// for every case except a CancelOrder failure immediately followed by a GetOrder failure
// on the very same order — a much smaller, genuinely irreducible window.

// KNOWN GAP (2026-07-09, not yet fixed): bracket orders (SL/TP) cannot be WAL'd the way
// entry/exit market orders are, because ExitOrderRequest carries no ClientOrderID field.
//
// The entry/exit path (this file, handleSignal) generates a clid and tracks it ON-LOOP
// BEFORE placing the order off-loop, then publishes KindOrderPlace to poslog as a WAL
// entry before the REST call — see the "Generate the client order id and track it
// BEFORE placing the order" comment above and runPlaceREST's "Publish KindOrderPlace to
// poslog as WAL before we make the exchange REST calls" line. This means a WS fill that
// races ahead of the REST response still routes correctly (the clid is already known),
// and a crash between "placed" and "REST returned" still leaves a durable trail.
//
// scheduleBracketPlacement (hand_fills.go) has none of this: it calls PlaceExitOrders
// and only learns the real order IDs from the result, tracking them (h.trackOrder) and
// writing KindBracketPlaced to poslog AFTER the REST call returns successfully. A WS
// fill or crash in the placement window has nothing to route to or recover from — the
// bracket is placed at the exchange with zero durable trace until the REST call comes
// back.
//
// This is NOT an exchange limitation — checked all four adapters. Binance spot OCO
// (binance/act/orders.go, NewCreateOCOService) never calls the SDK's available
// ListClientOrderId/LimitClientOrderId/StopClientOrderId setters. Binance futures algo
// orders (fbinance/act/orders.go, NewCreateAlgoOrderService) never call the SDK's
// available client-order-id setter either. OKX and Bybit conditional/algo orders support
// client-supplied IDs too. The gap is entirely in ExitOrderRequest/ExitOrderPlacer: the
// interface was designed after the entry/exit clid-first pattern already existed (see
// CLIENT_ORDER_ID.md) and was never retrofitted to carry one.
//
// Not fixed. Retrofitting would mean: add ClientOrderID to ExitOrderRequest, generate +
// track it in scheduleBracketPlacement before calling PlaceExitOrders (same discipline as
// handleSignal), wire the setter through all four adapters, and decide what "WAL" means
// for a bracket specifically — unlike an entry/exit, a bracket is TWO orders (SL + TP) on
// most exchanges, so the WAL entry would need to represent a pending pair, not a single
// clid.

// IMPLEMENTED (2026-07-09): the freeQty.IsZero() special case in runPlaceREST's
// balance-check block used to be a separate tier from the freeQty.LessThan(enough)
// "all-or-orphan" check further down — the diagnostic (query the bracket's own order
// status to recover a real OCO fill price, or confirm a helm-initiated cancel just
// hasn't unfrozen yet) only ran when freeQty was EXACTLY zero. Any insufficient-but-
// nonzero balance — which is exactly what the multi-hand-shared-wallet gap noted
// elsewhere in this file would produce (another hand's lock eating part, but not all,
// of the free balance) — used to skip the diagnostic entirely and orphan immediately,
// even though the same case-A/case-B recovery could apply just as well there.
//
// Now gated on `freeQty.LessThan(enough)` instead of `freeQty.IsZero()`, so the
// diagnostic runs for any shortfall, not just an exact-zero one; the exact-zero check
// is still the deciding line for positionGoneAtExchange vs. falling through to the
// all-or-orphan check below, since that distinction (dust-exit vs. orphan) is still
// meaningful downstream in applyPlaceResult.
//
// Deliberately scoped narrow — NOT bundled with the other two notes above it in this
// same change:
//  1. The "confirmed still resting" branch (distinguishing a successful GetOrder that
//     shows the bracket is still live from a GetOrder that errored) is still missing —
//     "anything else / error" still falls straight through to the shortfall check below
//     rather than being treated as "definitely not gone." That's a separate, slightly
//     riskier behavioral change (it changes what happens on the still-resting path, not
//     just when the diagnostic runs) and stays deferred.
//  2. Bracket ClientOrderID/WAL is untouched — bigger, cross-adapter change.
// This change only widens WHEN the existing diagnostic runs; it does not change what any
// branch of the diagnostic itself decides. Smallest safe slice of the three.

// KNOWN GAP (2026-07-09, not yet fixed): checkExits can fire a second, duplicate exit
// signal for a leg whose first exit is still in flight, when that leg has no
// exchange-side bracket.
//
// checkExits (hand_exits.go) skips its local SL/TP trigger only when a bracket exists:
//
//	if len(el.ExchangeOrderIDs) > 0 {
//		continue // exchange-side orders will close the position
//	}
//
// For a leg with NO bracket — PlaceExitOrders failed earlier (see the
// "position now relies on the local monitor only" log in scheduleBracketPlacement), or
// the bracket simply hasn't been placed yet — this guard never fires. checkExits only
// additionally checks h.pos.ActiveCount() > 0 (true for any non-Idle phase, including
// PhaseExiting — the same phase-blindness already noted for the Gate 3 TOCTOU race) and
// whether price is still past the SL/TP level. Neither check knows an exit for this
// exact leg is already in flight off-loop in a runPlaceREST goroutine.
//
// If that first exit is slow — stuck in the exit-retry loop, a clock-skew resync, or
// just a slow network — for longer than one poll tick (30s), checkExits fires again on
// the next tick. handleSignal processes it immediately (the run loop is free; it only
// spawned a goroutine for the first exit and returned), spawning a SECOND runPlaceREST
// for the same symbol and the same poslog qty (the leg hasn't closed yet, so its tracked
// qty hasn't changed).
//
// The balance-check that follows (the freeQty.LessThan(enough) block above) is a
// natural but non-atomic safety net here: it only protects if one PlaceOrder call has
// already landed and reduced the wallet by the time the other checks its balance. Two
// concurrent balance-checks that both read the wallet before either PlaceOrder executes
// will both see "enough" and both proceed — whichever's PlaceOrder lands first wins,
// but the other has already committed to placing its own order too, not merely lost a
// race it could still back out of.
//
// Not fixed. Would need checkExits to know "an exit is already pending for this symbol"
// before firing — e.g. a per-symbol in-flight-exit flag set when handleSignal spawns an
// exit's runPlaceREST goroutine and cleared in applyPlaceResult, checked by checkExits
// alongside the existing ExchangeOrderIDs guard. Same family as the Gate 3 TOCTOU gap
// (runtime/core/risk/manager.go) — both are "leg phase should have gated this but
// didn't" — worth revisiting together.

// STRUCTURAL NOTE (2026-07-09): the pre-exit OCO-cancel + balance-check block (the spot
// sell-exit branch at the top of runPlaceREST, roughly lines 635-793) has accumulated
// more loosely-specified behavior than any other single block in this file — five
// separate notes above touch it (cancel-failure ambiguity, the IsZero/LessThan gate,
// the "confirmed still resting" branch that's still missing, bracket ClientOrderID/WAL,
// and the checkExits race). None of that behavior is under test — it's ~160 lines
// inlined into runPlaceREST, reachable only through a live (or fully mocked) exchange,
// with no seam to unit-test the cancel/diagnose/decide logic in isolation from the rest
// of order placement.
//
// Worth pulling into its own function — something like
// `resolveSpotExitBalance(ctx, pp, bracketIDs) (freeQty decimal.Decimal, outcome
// exitBalanceOutcome)` — with an explicit result type instead of writing directly onto
// pp.positionGoneAtExchange / pp.orphanInsufficientFree / pp.ocoFillPrice from deep
// inside a nested if/for. That would (a) make the five gaps above independently
// testable against a fake Exchange instead of requiring a real one, and (b) force the
// "confirmed still resting" case to become a real, named outcome instead of silently
// falling through whatever the surrounding code happens to do next. Not done tonight —
// this is a refactor, not a fix, and refactoring the exact block this many open
// questions are still attached to, right before defending it, is its own risk. Revisit
// once the behavior itself is settled, not before.

// CONFIRMED IN PRODUCTION (2026-07-09, not yet fixed): the pre-exit bracket cancel loop
// (spot sell-exit branch, the `for _, id := range bracketIDs { CancelOrder(...) }` right
// before the balance-check block) logs a spurious WARN on every single spot exit that
// had a 2-leg bracket (SL + TP). Real prod log sample, ETHUSDT, same hand, 5 separate
// exits across 2026-07-03 through 2026-07-07, same shape every time:
//
//	INFO "hand: pre-exit bracket cancelled" cancelled=[legA, legB]   (both IDs listed)
//	WARN "hand: pre-exit bracket cancel" order_id=legB
//	     err="<APIError> code=-2011, msg=Unknown order sent."
//
// legA cancels cleanly. legB's explicit cancel then fails with Binance -2011 ("Unknown
// order sent" — the order no longer exists) EVERY time, with no exceptions in the
// sample. This confirms Binance auto-cancels the sibling leg of a spot OCO when the
// other leg is cancelled — by the time this loop reaches legB, the exchange has already
// removed it. The loop's second CancelOrder call is not just non-atomic (see the
// "không thể hủy cả cụm" note above about CancelOCOService) — for Binance spot
// specifically, it is a call that is *expected* to fail on every 2-leg bracket cancel,
// logged at WARN as if it were a real problem each time. That's log noise on the
// majority-case exit path, and it dilutes the same log line's signal value for a case
// where CancelOrder genuinely does fail for an unrelated reason.
//
// Also relevant to the cancel-ambiguity note further up this file: -2011 specifically is
// NOT the ambiguous "don't know if it landed" case that note is about — it is Binance's
// clean, unambiguous "this order does not exist" signal, same family as the -2013
// handling already done in GetOrder (binance/act/orders.go, isBinanceCode helper) that
// maps to a "not_found" sentinel instead of a bare error. CancelOrder has no equivalent
// treatment: every caller sees a generic error regardless of whether it means "genuinely
// failed" or "already gone, nothing to do."
//
// Not fixed tonight. Smallest correct fix: classify CancelOrder errors the same way
// GetOrder already does (isBinanceCode(err, -2011) -> treat as success, don't warn) —
// narrow, exchange-specific, low-risk. Larger fix: adopt CancelOCOService (Binance) /
// the equivalent group-cancel primitive per exchange where available, which would avoid
// attempting the second cancel at all instead of catching its expected failure after the
// fact — see the ClientOrderID/WAL note and the "không thể hủy cả cụm" discussion above.

// STRUCTURAL NOTE (2026-07-09): the group-cancel fix above (CancelOCOService /
// OrderListID) cannot be done as a local change inside the cancel loop — the storage
// pipeline has nowhere to hold a group/parent id at all, at any layer:
//
//	exchange.ExitOrderResult   { OrderIDs []string }               (exchange.go)
//	exitLevel.ExchangeOrderIDs []string                             (hand.go:225)
//	poslog KindBracketPlaced payload                                (writes the same slice)
//
// All three only ever carry a flat list of individual leg order IDs. binance/act/
// orders.go:153-157 already receives resp.OrderListID from Binance's own OCO placement
// response and discards it on the spot — it never reaches ExitOrderResult, so there is
// currently no path for it to reach exitLevel or poslog even if CancelOCOService were
// wired in tomorrow.
//
// A real fix needs a group/parent id field threaded through all three layers together
// (e.g. exitLevel.GroupID string, same on ExitOrderResult and the poslog payload), not
// just a change to the cancel loop. Bigger than the log-noise fix noted above — same
// shape of gap as the bracket ClientOrderID/WAL note (interface designed before this
// need existed, never retrofitted). Worth doing together with that one, since both touch
// ExitOrderResult and exitLevel at the same time.
