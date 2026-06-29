package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/clid"
	"mallow/helm/internal/runtime/position"
)

// ReconcileAction describes what the reconciler did for a hand.
type ReconcileAction string

const (
	// ReconcileRestored : hand had an open position; confirmed still live at exchange.
	ReconcileRestored ReconcileAction = "restored"
	// ReconcileFillApplied : pending order was filled while app was down; fill event emitted.
	ReconcileFillApplied ReconcileAction = "fill_applied"
	// ReconcileCancelled : pending order was canceled or rejected at exchange.
	ReconcileCancelled ReconcileAction = "order_cancelled"
	// ReconcileExternalClose : position was closed externally (liquidation, manual).
	ReconcileExternalClose ReconcileAction = "external_close"
	// ReconcileSkipped : hand was idle; nothing to do.
	ReconcileSkipped ReconcileAction = "skipped"
	// ReconcileFailed : could not determine state; hand left stopped for manual review.
	ReconcileFailed ReconcileAction = "failed"
)

// HandReconcileResult is the per-hand outcome of startup reconciliation.
type HandReconcileResult struct {
	HandID string
	Phase  position.Phase // aggregate phase AFTER reconciliation
	Action ReconcileAction
	Err    error
}

// Reconciler rebuilds in-memory hand state from the poslog and cross-references
// the exchange on startup. It must run before any hand processes signals.
type Reconciler interface {
	// Reconcile runs startup reconciliation for all running/paused hands under orch.
	// It blocks until every hand has been checked.
	Reconcile(ctx context.Context, orch *HelmRuntime) []HandReconcileResult

	// ReconcileSingle reconciles one hand on-demand by fetching fresh exchange state.
	// Used when a hand is restarted after an extended downtime gap so fills and
	// external closes that occurred while the hand was stopped are applied before
	// the first signal is processed.
	ReconcileSingle(ctx context.Context, orch *HelmRuntime, hand *Hand) HandReconcileResult
}

// reconcileHandTimeout caps exchange API time per hand during reconcile.
// One slow exchange must not block all remaining hands.
const reconcileHandTimeout = 30 * time.Second

// equityDriftThreshold is the fractional tolerance for post-reconcile equity check.
// A drift above 1% of exchange equity triggers CodeReconcileEquityDrift.
const equityDriftThreshold = 0.01

// ── Default implementation ────────────────────────────────────────────────────

// DefaultReconciler implements Reconciler using poslog + exchange queries.
type DefaultReconciler struct {
	log poslog.Log
}

func NewReconciler(log poslog.Log) *DefaultReconciler {
	return &DefaultReconciler{log: log}
}

func (r *DefaultReconciler) Reconcile(ctx context.Context, orch *HelmRuntime) []HandReconcileResult {
	orch.mu.RLock()
	hands := make([]*Hand, 0, len(orch.hands))
	for _, e := range orch.hands {
		hands = append(hands, e.h)
	}
	orch.mu.RUnlock()

	// Batch-fetch open orders and positions once per reconcile run.
	// On failure, nil is passed to reconcileHand — flat hands are still reconciled
	// correctly (they need no exchange data); hands with active legs are marked failed
	// individually rather than failing the entire batch.
	exchangeOrders, errOrders := r.fetchOpenOrders(ctx, orch)
	exchangePositions, errPositions := r.fetchPositions(ctx, orch)
	if errOrders != nil {
		slog.Error("reconcile: ListOpenOrders failed — hands with pending orders will be marked failed",
			"helm_id", orch.HelmID, "err", errOrders)
	}
	if errPositions != nil {
		slog.Error("reconcile: ListPositions failed — hands with open legs will be marked failed",
			"helm_id", orch.HelmID, "err", errPositions)
	}

	// Per-action counters for the summary event.
	counts := make(map[ReconcileAction]int, 6)

	results := make([]HandReconcileResult, 0, len(hands))
	for _, hand := range hands {
		// Each hand gets an independent timeout so one slow exchange API call
		// cannot block every remaining hand from being reconciled.
		handCtx, handCancel := context.WithTimeout(ctx, reconcileHandTimeout)
		res := r.reconcileHand(handCtx, orch, hand, exchangeOrders, exchangePositions, errOrders, errPositions)
		handCancel()

		results = append(results, res)
		counts[res.Action]++
		if res.Err != nil {
			slog.Error("reconcile failed", "hand_id", hand.id, "err", res.Err)
			hand.emitEvent(natsapi.HelmEvent{
				Code:   CodeReconcileFailed,
				Reason: res.Err.Error(),
				Msg:    "reconcile: failed — hand left stopped for manual review",
			})
		} else {
			slog.Info("reconcile", "hand_id", hand.id, "action", res.Action, "phase", res.Phase)
		}
	}

	// ── Summary event ────────────────────────────────────────────────────────
	// Helm-level (no HandID) so operators see one event for the whole reconcile
	// instead of N per-hand events only.
	orch.EmitEvent(natsapi.HelmEvent{
		Code: CodeReconcileComplete,
		Msg:  "reconcile: done",
		Reason: fmt.Sprintf(
			"hands=%d restored=%d fills=%d ext_close=%d cancelled=%d skipped=%d failed=%d",
			len(hands),
			counts[ReconcileRestored],
			counts[ReconcileFillApplied],
			counts[ReconcileExternalClose],
			counts[ReconcileCancelled],
			counts[ReconcileSkipped],
			counts[ReconcileFailed],
		),
	})

	// ── Post-reconcile equity drift check ────────────────────────────────────
	// Compare helm's calculated portfolio equity with the exchange's actual
	// balance. A drift > 1% means state was not fully recovered — alert the user.
	// Runs in a goroutine so it doesn't delay handSvc.StartAllHydrated().
	go func() {
		r.checkEquityDrift(ctx, orch)
	}()

	return results
}

// checkEquityDrift fetches the exchange account snapshot (if supported) and
// compares it with the helm portfolio equity. Emits CodeReconcileEquityDrift
// when the absolute difference exceeds equityDriftThreshold of exchange equity.
// Read-only — does NOT apply the snapshot to the portfolio.
func (r *DefaultReconciler) checkEquityDrift(ctx context.Context, orch *HelmRuntime) {
	syncer, ok := orch.Exchange.(exchange.AccountSyncer)
	if !ok {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	snap, err := syncer.SyncAccount(checkCtx, orch.Creds, nil)
	if err != nil {
		slog.Warn("reconcile: equity drift check — SyncAccount failed (non-fatal)",
			"helm_id", orch.HelmID, "err", err)
		return
	}
	if !snap.Equity.IsPositive() || !orch.Portfolio.Equity().IsPositive() {
		return
	}
	helmEquity := orch.Portfolio.Equity()
	drift := snap.Equity.Sub(helmEquity).Abs()
	threshold := snap.Equity.Mul(decimal.NewFromFloat(equityDriftThreshold))
	if drift.LessThan(threshold) {
		return
	}
	driftPct := drift.Div(snap.Equity).Mul(decimal.NewFromInt(100))
	slog.Warn("reconcile: equity drift detected",
		"helm_id", orch.HelmID,
		"exchange_equity", snap.Equity,
		"helm_equity", helmEquity,
		"drift_pct", driftPct,
	)
	orch.EmitEvent(natsapi.HelmEvent{
		Code: CodeReconcileEquityDrift,
		Reason: fmt.Sprintf("exchange=%.4f helm=%.4f drift=%.2f%%",
			snap.Equity.InexactFloat64(),
			helmEquity.InexactFloat64(),
			driftPct.InexactFloat64(),
		),
		Msg: "reconcile: equity drift detected — portfolio state may be out of sync",
	})
}

// ReconcileSingle reconciles one hand by fetching fresh exchange state on-demand.
// Used when a hand is restarted after an extended downtime gap. Applies any fills
// or external closes that occurred while the hand was stopped, using the same
// poslog+exchange logic as the startup reconciler.
func (r *DefaultReconciler) ReconcileSingle(ctx context.Context, orch *HelmRuntime, hand *Hand) HandReconcileResult {
	handCtx, cancel := context.WithTimeout(ctx, reconcileHandTimeout)
	defer cancel()

	openOrders, errOrders := r.fetchOpenOrders(handCtx, orch)
	if errOrders != nil {
		slog.Error("on-demand reconcile: ListOpenOrders failed", "hand_id", hand.id, "err", errOrders)
	}
	positions, errPositions := r.fetchPositions(handCtx, orch)
	if errPositions != nil {
		slog.Error("on-demand reconcile: ListPositions failed", "hand_id", hand.id, "err", errPositions)
	}

	res := r.reconcileHand(handCtx, orch, hand, openOrders, positions, errOrders, errPositions)
	if res.Err != nil {
		hand.emitEvent(natsapi.HelmEvent{
			Code:   CodeReconcileFailed,
			Reason: res.Err.Error(),
			Msg:    "reconcile: on-demand failed — hand state may be inconsistent",
		})
	} else {
		slog.Info("reconcile: on-demand done",
			"hand_id", hand.id, "action", res.Action, "phase", res.Phase)
	}
	return res
}

func (r *DefaultReconciler) reconcileHand(
	ctx context.Context,
	helmRuntime *HelmRuntime,
	hand *Hand,
	openOrders map[string]exchange.OrderResult, // nil when ListOpenOrders failed
	positions map[string]exchange.PositionResult, // nil when ListPositions failed
	errOrders, errPositions error,
) HandReconcileResult {
	result := HandReconcileResult{HandID: hand.id.String()}

	// Replay poslog to reconstruct all legs for this hand.
	events, err := r.log.ReplayHand(ctx, hand.helmID.String(), hand.id.String())
	if err != nil {
		result.Action = ReconcileFailed
		result.Err = fmt.Errorf("poslog replay: %w", err)
		return result
	}
	hp := position.ReplayHand(events, hand.cfg.pyramid, hand.cfg.maxUnits)

	if hp.IsFlat() {
		// No open position, but still restore historical PnL so realizedEquity() is correct.
		hand.RestorePnL(ctx, events)
		hand.RestoreCounters(ctx)
		result.Phase = position.PhaseIdle
		result.Action = ReconcileSkipped
		return result
	}

	// Reconcile each active leg independently.
	var lastAction ReconcileAction
	for _, leg := range hp.ActiveLegs() {
		action, err := r.reconcileLeg(ctx, helmRuntime, hand, leg, openOrders, positions, errOrders, errPositions)
		if err != nil {
			result.Action = ReconcileFailed
			result.Err = err
			return result
		}
		lastAction = action
	}

	// Re-replay to get the updated phase after any emitted events.
	events2, _ := r.log.ReplayHand(ctx, hand.helmID.String(), hand.id.String())
	hp2 := position.ReplayHand(events2, hand.cfg.pyramid, hand.cfg.maxUnits)

	primary := hp2.PrimaryLeg()
	if primary != nil {
		result.Phase = primary.Phase
	} else {
		result.Phase = position.PhaseIdle
	}
	result.Action = lastAction

	// Restore the confirmed open position into the portfolio.
	if !hp2.IsFlat() {
		pos := hp2.ToPosition(hand.id.String(), hand.helmID.String(), decimal.Zero)
		if p, ok := positions[pos.Symbol]; ok {
			hand.restorePosition(hp2, p.AvgPrice)
		}
	}

	// Restore PnL counters from poslog history so realizedEquity() reflects all
	// completed trades, not just the current open position.
	hand.RestorePnL(ctx, events2)
	hand.RestoreCounters(ctx)

	return result
}

// reconcileLeg handles one active leg: checks pending orders or confirms open positions.
// errOrders/errPositions are the errors from the batch fetch; nil maps mean that fetch failed.
// Each leg fails independently — other legs/hands are not affected.
func (r *DefaultReconciler) reconcileLeg(
	ctx context.Context,
	helmRuntime *HelmRuntime,
	hand *Hand,
	leg *position.LegState,
	openOrders map[string]exchange.OrderResult,
	positions map[string]exchange.PositionResult,
	errOrders, errPositions error,
) (ReconcileAction, error) {
	switch leg.Phase {
	case position.PhaseEntering, position.PhaseAdding, position.PhaseExiting:
		if openOrders == nil {
			return ReconcileFailed, fmt.Errorf("reconcile: pending order %s cannot be checked — ListOpenOrders failed: %w", leg.PendingOrderID, errOrders)
		}
		return r.reconcilePendingOrder(ctx, helmRuntime, hand, leg, openOrders)

	case position.PhaseOpen:
		if positions == nil {
			return ReconcileFailed, fmt.Errorf("reconcile: open leg %s cannot be confirmed — ListPositions failed: %w", leg.PositionID, errPositions)
		}
		return r.reconcileOpenLeg(ctx, helmRuntime, hand, leg, positions)

	default:
		return ReconcileSkipped, nil
	}
}

// reconcilePendingOrder handles a leg with a pending order at the exchange.
func (r *DefaultReconciler) reconcilePendingOrder(
	ctx context.Context,
	orch *HelmRuntime,
	hand *Hand,
	leg *position.LegState,
	openOrders map[string]exchange.OrderResult,
) (ReconcileAction, error) {
	orderID := leg.PendingOrderID

	// Look up by ClientOrderID if orderID is our clid.
	var exOrder exchange.OrderResult
	var stillOpen bool
	if clid.IsOurClid(orderID) {
		for _, o := range openOrders {
			if o.ClientOrderID == orderID {
				exOrder = o
				stillOpen = true
				break
			}
		}
	} else {
		exOrder, stillOpen = openOrders[orderID]
	}

	if stillOpen {
		// If we only had KindOrderPlace (so PendingOrderID was the ClientOrderID),
		// we must now emit KindOrderPlaced with the actual exchange OrderID.
		if clid.IsOurClid(orderID) {
			if err := r.emitOrderPlaced(ctx, hand, leg.PositionID, &exOrder, leg); err != nil {
				return ReconcileFailed, fmt.Errorf("emitOrderPlaced: %w", err)
			}
			orderID = exOrder.ID
		}

		// Restore order tracking map so that future WS fill events can be routed to this hand.
		orch.TrackOrder(orderID, hand.id.String())
		// If the exchange echoes our clid, restore clid routing too — WS fills carry the
		// clid and would otherwise route as orphan after a restart. See CLIENT_ORDER_ID.md.
		if clid.IsOurClid(exOrder.ClientOrderID) {
			orch.TrackOrder(exOrder.ClientOrderID, hand.id.String())
		}

		// Restore order into hand.orders so that pollOrders can check its status.
		// Use remaining qty (original − already filled) to avoid double-counting
		// any partial fills that arrived before the restart.
		hand.mu.Lock()
		alreadyTracked := false
		for _, o := range hand.orders {
			if o.ID == orderID {
				alreadyTracked = true
				break
			}
		}
		if !alreadyTracked {
			remainingQty := leg.Qty.Sub(exOrder.FilledQty)
			if !remainingQty.IsPositive() {
				remainingQty = leg.Qty // defensive: treat as fully open if exchange data is inconsistent
			}
			hand.orders = append(hand.orders, handdomain.Order{
				ID:         orderID,
				Symbol:     leg.Symbol,
				Side:       leg.Side,
				Qty:        remainingQty,
				Status:     "submitted",
				SubmitTime: leg.OpenedAt,
			})
		}
		hand.mu.Unlock()

		hand.emitEvent(natsapi.HelmEvent{
			Code:    CodeReconcileRestored,
			Symbol:  leg.Symbol,
			OrderID: orderID,
			Reason:  "pending order still open at exchange after restart",
			Msg:     "reconcile: order restored",
		})
		return ReconcileRestored, nil
	}

	// Order gone from open list — get terminal status.
	var termOrder *exchange.OrderResult
	var err error
	if clid.IsOurClid(orderID) {
		if cq, ok := orch.Exchange.(exchange.ClientOrderQuerier); ok {
			marketKind := exchange.MarketSpot
			if hand.cfg.futuresConfig != nil {
				marketKind = exchange.MarketFutures
			}
			termOrder, err = cq.GetOrderByClientOrderID(ctx, orch.Creds, leg.Symbol, marketKind, orderID)
			if err != nil {
				return ReconcileFailed, fmt.Errorf("GetOrderByClientOrderID %s: %w", orderID, err)
			}
			if termOrder == nil {
				// Order never reached the exchange. Emit cancelled event.
				if err := r.emitOrderCancelled(ctx, hand, leg.PositionID, orderID, "not_found_at_exchange"); err != nil {
					return ReconcileFailed, err
				}
				hand.emitEvent(natsapi.HelmEvent{
					Code:    CodeReconcileCancelled,
					Symbol:  leg.Symbol,
					OrderID: orderID,
					Reason:  "pre-flight order never reached the exchange",
					Msg:     "reconcile: pre-flight order cancelled",
				})
				return ReconcileCancelled, nil
			}
		} else {
			return ReconcileFailed, fmt.Errorf("reconcile: exchange %s does not support ClientOrderQuerier to lookup clid %s", orch.Exchange.Name(), orderID)
		}
	} else {
		termOrder, err = orch.Exchange.GetOrder(ctx, orch.Creds, orderID)
		if err != nil {
			return ReconcileFailed, fmt.Errorf("GetOrder %s: %w", orderID, err)
		}
	}

	// If we only had KindOrderPlace (so PendingOrderID was the ClientOrderID),
	// we must now emit KindOrderPlaced with the actual exchange OrderID before terminal event.
	if clid.IsOurClid(orderID) {
		if err := r.emitOrderPlaced(ctx, hand, leg.PositionID, termOrder, leg); err != nil {
			return ReconcileFailed, fmt.Errorf("emitOrderPlaced: %w", err)
		}
		orderID = termOrder.ID
	}

	switch termOrder.Status {
	case "filled":
		if err := r.emitOrderFilled(ctx, hand, leg.PositionID, termOrder, "reconcile"); err != nil {
			return ReconcileFailed, err
		}
		hand.emitEvent(natsapi.HelmEvent{
			Code:    CodeReconcileFillApplied,
			Symbol:  leg.Symbol,
			OrderID: orderID,
			Qty:     termOrder.FilledQty,
			Price:   termOrder.FilledAvg,
			Reason:  "fill missed during downtime — applied retroactively",
			Msg:     "reconcile: fill applied",
		})
		return ReconcileFillApplied, nil

	case "cancelled", "rejected", "expired":
		if err := r.emitOrderCancelled(ctx, hand, leg.PositionID, orderID, termOrder.Status); err != nil {
			return ReconcileFailed, err
		}
		hand.emitEvent(natsapi.HelmEvent{
			Code:    CodeReconcileCancelled,
			Symbol:  leg.Symbol,
			OrderID: orderID,
			Reason:  termOrder.Status + " during downtime",
			Msg:     "reconcile: order cancelled",
		})
		return ReconcileCancelled, nil

	case "partially_filled", "new", "accepted", "pending_new", "submitted":
		// Order still open (possibly missed by the open-orders batch fetch due to a
		// brief exchange inconsistency). Restore tracking so pollOrders keeps watching it.
		orch.TrackOrder(orderID, hand.id.String())
		if clid.IsOurClid(termOrder.ClientOrderID) {
			orch.TrackOrder(termOrder.ClientOrderID, hand.id.String())
		}
		hand.mu.Lock()
		alreadyTracked := false
		for _, o := range hand.orders {
			if o.ID == orderID {
				alreadyTracked = true
				break
			}
		}
		if !alreadyTracked {
			remainingQty := leg.Qty.Sub(termOrder.FilledQty)
			if !remainingQty.IsPositive() {
				remainingQty = leg.Qty // defensive: treat as fully open if data is inconsistent
			}
			hand.orders = append(hand.orders, handdomain.Order{
				ID:         orderID,
				Symbol:     leg.Symbol,
				Side:       leg.Side,
				Qty:        remainingQty,
				Status:     termOrder.Status,
				FilledQty:  termOrder.FilledQty,
				FilledAvg:  termOrder.FilledAvg,
				SubmitTime: leg.OpenedAt,
			})
		}
		hand.mu.Unlock()
		slog.Info("reconcile: restored partially-open order for polling",
			"hand_id", hand.id, "order_id", orderID, "status", termOrder.Status,
			"filled_qty", termOrder.FilledQty, "original_qty", leg.Qty)
		hand.emitEvent(natsapi.HelmEvent{
			Code:    CodeReconcileRestored,
			Symbol:  leg.Symbol,
			OrderID: orderID,
			Reason:  termOrder.Status + " — restored for polling",
			Msg:     "reconcile: order restored",
		})
		return ReconcileRestored, nil

	default:
		return ReconcileFailed, fmt.Errorf("unexpected order status %q for order %s", termOrder.Status, orderID)
	}
}

// reconcileOpenLeg confirms an open leg still exists at the exchange.
// When the position is gone, it first tries to recover a bracket fill (SL/TP triggered
// during downtime) so the trade is recorded with the real execution price. Falls back to
// emitExternalClose (price=0, no PnL) only when no bracket fill can be found.
//
// Dust-after-OCO case: the exchange may report a tiny residual balance for the symbol
// even after an OCO bracket fully executed (rounding at fill, minimum lot mismatch).
// When bracket order IDs are known, always check them — a "filled" bracket status means
// the position was closed by the OCO regardless of any dust, and we should record the real
// trade rather than restoring the position and letting RecoverBrackets handle it later.
func (r *DefaultReconciler) reconcileOpenLeg(
	ctx context.Context,
	orch *HelmRuntime,
	hand *Hand,
	leg *position.LegState,
	positions map[string]exchange.PositionResult,
) (ReconcileAction, error) {
	pos, exists := positions[leg.Symbol]
	if !exists || pos.Qty.IsZero() {
		// Position fully gone — try bracket fill recovery first.
		if action, err := r.tryRecoverBracketFill(ctx, orch, hand, leg); action != "" {
			return action, err
		}

		// No bracket fill found — cancel any dangling OCO orders and record as external close.
		hand.mu.Lock()
		hand.cancelExitOrders(ctx, leg.Symbol, "")
		hand.mu.Unlock()

		if err := r.emitExternalClose(ctx, hand, leg); err != nil {
			return ReconcileFailed, err
		}
		hand.emitEvent(natsapi.HelmEvent{
			Code:   CodeReconcileExternalClose,
			Symbol: leg.Symbol,
			Reason: "position not found at exchange after restart — closed externally during downtime",
			Msg:    "reconcile: position externally closed",
		})
		return ReconcileExternalClose, nil
	}

	// Position balance exists at exchange (may be full size or dust after partial OCO fill).
	// Check bracket orders first: a "filled" bracket means the OCO executed during downtime
	// and only residual dust remains. Record the real trade now so RecoverBrackets does not
	// incorrectly treat the TP auto-cancel as an external close.
	if len(leg.ExchangeOrderIDs) > 0 {
		if action, err := r.tryRecoverBracketFill(ctx, orch, hand, leg); action != "" {
			hand.emitEvent(natsapi.HelmEvent{
				Code:   CodeReconcileFillApplied,
				Symbol: leg.Symbol,
				Qty:    pos.Qty,
				Reason: "bracket OCO triggered during downtime — position closed (residual dust may remain at exchange)",
				Msg:    "reconcile: bracket fill recovered; position closed",
			})
			return action, err
		}
	}

	// No bracket fill → position is genuinely still open.
	hand.emitEvent(natsapi.HelmEvent{
		Code:   CodeReconcileRestored,
		Symbol: leg.Symbol,
		Qty:    pos.Qty,
		Price:  pos.AvgPrice,
		Reason: "open position confirmed at exchange after restart",
		Msg:    "reconcile: position restored",
	})
	return ReconcileRestored, nil
}

// tryRecoverBracketFill queries each bracket order ID stored in the poslog to find a fill
// that occurred while helm was down. When found it emits KindPositionClosed with the real
// execution price and generates a trade record, giving accurate PnL instead of the zero-price
// external-close fallback.
//
// Returns ("", nil) when no filled bracket is found so the caller can fall back normally.
func (r *DefaultReconciler) tryRecoverBracketFill(
	ctx context.Context,
	orch *HelmRuntime,
	hand *Hand,
	leg *position.LegState,
) (ReconcileAction, error) {
	if len(leg.ExchangeOrderIDs) == 0 {
		return "", nil
	}

	for _, orderID := range leg.ExchangeOrderIDs {
		exOrder, err := orch.Exchange.GetOrder(ctx, orch.Creds, orderID)
		if err != nil || exOrder == nil {
			slog.Debug("reconcile: bracket GetOrder failed or not found",
				"hand_id", hand.id, "order_id", orderID, "err", err)
			continue
		}
		if exOrder.Status != "filled" || !exOrder.FilledQty.IsPositive() || !exOrder.FilledAvg.IsPositive() {
			continue
		}

		// Determine exit reason from exchange tag, falling back to price comparison.
		exitReason := "external"
		switch exOrder.Tag {
		case "tp":
			exitReason = "tp"
		case "sl":
			exitReason = "sl"
		default:
			if leg.TakeProfit.IsPositive() && exOrder.FilledAvg.GreaterThanOrEqual(leg.TakeProfit) {
				exitReason = "tp"
			} else if leg.StopLoss.IsPositive() && exOrder.FilledAvg.LessThanOrEqual(leg.StopLoss) {
				exitReason = "sl"
			}
		}

		var pnl decimal.Decimal
		if leg.EntryPrice.IsPositive() {
			if leg.Side == "buy" {
				pnl = exOrder.FilledAvg.Sub(leg.EntryPrice).Mul(exOrder.FilledQty)
			} else {
				pnl = leg.EntryPrice.Sub(exOrder.FilledAvg).Mul(exOrder.FilledQty)
			}
		}

		if err := r.emitBracketFill(ctx, hand, leg, exOrder, exitReason, pnl); err != nil {
			return ReconcileFailed, fmt.Errorf("reconcile: bracket fill recovery failed: %w", err)
		}

		slog.Info("reconcile: bracket fill recovered",
			"hand_id", hand.id, "symbol", leg.Symbol,
			"order_id", orderID, "exit_reason", exitReason,
			"fill_price", exOrder.FilledAvg, "fill_qty", exOrder.FilledQty, "pnl", pnl)
		hand.emitEvent(natsapi.HelmEvent{
			Code:    CodeReconcileFillApplied,
			Symbol:  leg.Symbol,
			OrderID: orderID,
			Qty:     exOrder.FilledQty,
			Price:   exOrder.FilledAvg,
			Reason:  fmt.Sprintf("bracket %s filled during downtime — recovered with real fill price", exitReason),
			Msg:     "reconcile: bracket fill recovered",
		})
		return ReconcileFillApplied, nil
	}

	return "", nil
}

// emitBracketFill writes KindPositionClosed to the poslog with real fill data and appends
// a trade record. Used by tryRecoverBracketFill to produce accurate PnL when a bracket
// order triggered while helm was down.
func (r *DefaultReconciler) emitBracketFill(
	ctx context.Context,
	hand *Hand,
	leg *position.LegState,
	exOrder *exchange.OrderResult,
	exitReason string,
	pnl decimal.Decimal,
) error {
	cp := poslog.PositionClosedPayload{
		OrderID:         leg.PositionID,
		Symbol:          leg.Symbol,
		Side:            leg.Side,
		Qty:             exOrder.FilledQty.String(),
		EntryPrice:      leg.EntryPrice.String(),
		EntryAt:         leg.OpenedAt,
		ClosePrice:      exOrder.FilledAvg.String(),
		RealizedPnL:     pnl.String(),
		ExitReason:      exitReason,
		EntryOrderID:    leg.PositionID,
		ExitOrderID:     exOrder.ID,
		DeployedCapital: decimalToString(leg.DeployedCapital),
	}
	if leg.StopLoss.IsPositive() {
		cp.StopLossPrice = leg.StopLoss.String()
	}
	if leg.TakeProfit.IsPositive() {
		cp.TakeProfitPrice = leg.TakeProfit.String()
	}

	payload, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	// Deterministic ID: safe to re-run if reconciler crashes mid-write.
	// JetStream dedup prevents double-recording on retry.
	if err := r.log.Publish(ctx, poslog.Event{
		ID:         hand.id.String() + "_bracketfill_" + leg.PositionID + "_" + exOrder.ID,
		HandID:     hand.id.String(),
		HelmID:     hand.helmID.String(),
		PositionID: leg.PositionID,
		Kind:       poslog.KindPositionClosed,
		Payload:    payload,
		At:         time.Now().UTC(),
	}); err != nil {
		return err
	}

	// Persist the trade record (no-op when TradeLog is not configured).
	hand.appendTradeRecord(ctx, cp, decimal.Zero, time.Now().UTC())
	return nil
}

// ── poslog emit helpers ───────────────────────────────────────────────────────

func (r *DefaultReconciler) emitOrderPlaced(
	ctx context.Context,
	hand *Hand,
	positionID string,
	exOrder *exchange.OrderResult,
	leg *position.LegState,
) error {
	isClose := leg.Phase == position.PhaseExiting
	isPyramidAdd := leg.Phase == position.PhaseAdding
	priceStr := leg.Price
	if priceStr == "" {
		priceStr = "0"
	}
	orderTypeStr := leg.OrderType
	if orderTypeStr == "" {
		orderTypeStr = "market"
	}
	payload, _ := json.Marshal(poslog.OrderPlacedPayload{
		OrderID:       exOrder.ID,
		ClientOrderID: exOrder.ClientOrderID,
		Symbol:        exOrder.Symbol,
		Side:          string(exOrder.Side),
		Qty:           exOrder.Qty.String(),
		Price:         priceStr,
		OrderType:     orderTypeStr,
		StopLoss:      leg.StopLoss.String(),
		TakeProfit:    leg.TakeProfit.String(),
		IsPyramidAdd:  isPyramidAdd,
		IsClose:       isClose,
	})
	return r.log.Publish(ctx, poslog.Event{
		ID:         exOrder.ID,
		HandID:     hand.id.String(),
		HelmID:     hand.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderPlaced,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}

func (r *DefaultReconciler) emitOrderFilled(
	ctx context.Context,
	hand *Hand,
	positionID string,
	exOrder *exchange.OrderResult,
	source string,
) error {
	payload, _ := json.Marshal(poslog.OrderFilledPayload{
		OrderID:   exOrder.ID,
		FillPrice: exOrder.FilledAvg.String(),
		FillQty:   exOrder.FilledQty.String(),
		Source:    source,
	})
	return r.log.Publish(ctx, poslog.Event{
		ID:         exOrder.ID + "_filled",
		HandID:     hand.id.String(),
		HelmID:     hand.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderFilled,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}

func (r *DefaultReconciler) emitOrderCancelled(
	ctx context.Context,
	hand *Hand,
	positionID, orderID, reason string,
) error {
	payload, _ := json.Marshal(poslog.OrderCancelledPayload{
		OrderID: orderID,
		Reason:  reason,
	})
	return r.log.Publish(ctx, poslog.Event{
		ID:         orderID + "_cancelled",
		HandID:     hand.id.String(),
		HelmID:     hand.helmID.String(),
		PositionID: positionID,
		Kind:       poslog.KindOrderCancelled,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}

func (r *DefaultReconciler) emitExternalClose(
	ctx context.Context,
	hand *Hand,
	leg *position.LegState,
) error {
	payload, _ := json.Marshal(poslog.PositionClosedPayload{
		OrderID:     leg.PendingOrderID,
		ClosePrice:  decimal.Zero.String(),
		RealizedPnL: decimal.Zero.String(),
		ExitReason:  "external",
	})
	return r.log.Publish(ctx, poslog.Event{
		// Deterministic ID: reconciler may run multiple times for the same leg
		// (e.g. crash during reconcile itself). Same position_id → same dedup key
		// → JetStream discards the duplicate, replay stays correct.
		ID:         hand.id.String() + "_ext_" + leg.PositionID,
		HandID:     hand.id.String(),
		HelmID:     hand.helmID.String(),
		PositionID: leg.PositionID,
		Kind:       poslog.KindPositionClosed,
		Payload:    payload,
		At:         time.Now().UTC(),
	})
}

// ── Exchange batch helpers ────────────────────────────────────────────────────

func (r *DefaultReconciler) fetchOpenOrders(ctx context.Context, orch *HelmRuntime) (map[string]exchange.OrderResult, error) {
	orders, err := orch.Exchange.ListOpenOrders(ctx, orch.Creds, "")
	if err != nil {
		slog.Warn("reconcile: ListOpenOrders failed", "exchange", orch.Exchange.Name(), "err", err)
		return nil, err
	}
	m := make(map[string]exchange.OrderResult, len(orders))
	for _, o := range orders {
		m[o.ID] = o
	}
	return m, nil
}

func (r *DefaultReconciler) fetchPositions(ctx context.Context, orch *HelmRuntime) (map[string]exchange.PositionResult, error) {
	positions, err := orch.Exchange.ListPositions(ctx, orch.Creds)
	if err != nil {
		slog.Warn("reconcile: ListPositions failed", "exchange", orch.Exchange.Name(), "err", err)
		return nil, err
	}
	m := make(map[string]exchange.PositionResult, len(positions))
	for _, p := range positions {
		m[p.Symbol] = p
	}
	return m, nil
}
