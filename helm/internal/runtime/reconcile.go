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
}

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
	for _, h := range orch.hands {
		hands = append(hands, h)
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

	results := make([]HandReconcileResult, 0, len(hands))
	for _, hand := range hands {
		res := r.reconcileHand(ctx, orch, hand, exchangeOrders, exchangePositions, errOrders, errPositions)
		results = append(results, res)
		if res.Err != nil {
			slog.Error("reconcile failed", "hand_id", hand.id, "err", res.Err)
		} else {
			slog.Info("reconcile", "hand_id", hand.id, "action", res.Action, "phase", res.Phase)
		}
	}
	return results
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

	// Fast path: order still open at exchange — nothing missed.
	if exOrder, stillOpen := openOrders[orderID]; stillOpen {
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

		return ReconcileRestored, nil
	}

	// Order gone from open list — get terminal status.
	exOrder, err := orch.Exchange.GetOrder(ctx, orch.Creds, orderID)
	if err != nil {
		return ReconcileFailed, fmt.Errorf("GetOrder %s: %w", orderID, err)
	}

	switch exOrder.Status {
	case "filled":
		if err := r.emitOrderFilled(ctx, hand, leg.PositionID, exOrder, "reconcile"); err != nil {
			return ReconcileFailed, err
		}
		return ReconcileFillApplied, nil

	case "cancelled", "rejected", "expired":
		if err := r.emitOrderCancelled(ctx, hand, leg.PositionID, orderID, exOrder.Status); err != nil {
			return ReconcileFailed, err
		}
		return ReconcileCancelled, nil

	case "partially_filled", "new", "accepted", "pending_new", "submitted":
		// Order still open (possibly missed by the open-orders batch fetch due to a
		// brief exchange inconsistency). Restore tracking so pollOrders keeps watching it.
		orch.TrackOrder(orderID, hand.id.String())
		if clid.IsOurClid(exOrder.ClientOrderID) {
			orch.TrackOrder(exOrder.ClientOrderID, hand.id.String())
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
			remainingQty := leg.Qty.Sub(exOrder.FilledQty)
			if !remainingQty.IsPositive() {
				remainingQty = leg.Qty // defensive: treat as fully open if data is inconsistent
			}
			hand.orders = append(hand.orders, handdomain.Order{
				ID:         orderID,
				Symbol:     leg.Symbol,
				Side:       leg.Side,
				Qty:        remainingQty,
				Status:     exOrder.Status,
				FilledQty:  exOrder.FilledQty,
				FilledAvg:  exOrder.FilledAvg,
				SubmitTime: leg.OpenedAt,
			})
		}
		hand.mu.Unlock()
		slog.Info("reconcile: restored partially-open order for polling",
			"hand_id", hand.id, "order_id", orderID, "status", exOrder.Status,
			"filled_qty", exOrder.FilledQty, "original_qty", leg.Qty)
		return ReconcileRestored, nil

	default:
		return ReconcileFailed, fmt.Errorf("unexpected order status %q for order %s", exOrder.Status, orderID)
	}
}

// reconcileOpenLeg confirms an open leg still exists at the exchange.
func (r *DefaultReconciler) reconcileOpenLeg(
	ctx context.Context,
	_ *HelmRuntime,
	hand *Hand,
	leg *position.LegState,
	positions map[string]exchange.PositionResult,
) (ReconcileAction, error) {
	pos, exists := positions[leg.Symbol]
	if !exists || pos.Qty.IsZero() {
		// Position was closed externally while app was down.
		if err := r.emitExternalClose(ctx, hand, leg); err != nil {
			return ReconcileFailed, err
		}
		return ReconcileExternalClose, nil
	}
	return ReconcileRestored, nil
}

// ── poslog emit helpers ───────────────────────────────────────────────────────

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
