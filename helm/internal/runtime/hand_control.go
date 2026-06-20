package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/safe"
)

// ---------------------------------------------------------------------------
// Lifecycle — called by Registry on behalf of user actions
// ---------------------------------------------------------------------------

// emitEvent publishes a behavioral event via HelmRuntime (slog + JetStream + eventlog persister)
// and, when the test event bus is active, broadcasts a copy to all subscribers.
func (h *Hand) emitEvent(ev natsapi.HelmEvent) {
	ev.HandID = h.id.String()
	h.helmRuntime.EmitEvent(ev)
	if h.eventBus != nil {
		h.eventBus.publish(ev)
	}
}

// onDemandReconcileGap is the minimum time a hand must have been stopped before
// Start() triggers an on-demand reconcile. Covers fills and position changes that
// occurred while the hand was stopped.
// Short stop-restart cycles (test, cascade-pause/resume) are excluded.
const onDemandReconcileGap = 30 * time.Second

// Start spawns the bot's run-loop goroutine.
// If the hand was previously stopped for longer than onDemandReconcileGap, it also
// spawns a background reconcile goroutine so any fills missed during the downtime
// are applied before the first signal arrives.
func (h *Hand) Start() {
	h.mu.Lock()
	if h.running {
		h.mu.Unlock()
		return
	}
	h.running = true
	h.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	h.cancel = cancel
	h.health.Status = HealthRunning
	now := time.Now().UTC()
	h.health.StartedAt = &now
	// Capture the gap while the lock is held, then release before spawning goroutines.
	needsReconcile := !h.stoppedAt.IsZero() && now.Sub(h.stoppedAt) > onDemandReconcileGap
	h.mu.Unlock()

	go h.run(ctx)

	// On-demand reconcile: catch fills and external closes that occurred while the
	// hand was stopped. Runs concurrently with the run-loop (which waits for the
	// first signal) — safe because the run-loop holds no orders at this point.
	if needsReconcile {
		go func() {
			defer safe.Recover()
			slog.Info("hand: on-demand reconcile — restarted after gap",
				"hand_id", h.id, "gap", now.Sub(h.stoppedAt).Truncate(time.Second))
			h.helmRuntime.ReconcileHand(context.Background(), h)
		}()
	}

	slog.Info("hand started", "hand_id", h.id, "exchange", h.helmRuntime.Exchange.Name())
	h.emitEvent(natsapi.HelmEvent{
		Code:   CodeHandStarted,
		Msg:    "hand: started",
		Reason: "if the strategy emits no stop_loss, a default will be applied (ATR×5; fallback 8% fixed)",
	})
}

// Stop cancels the run-loop and waits for it to exit.
func (h *Hand) Stop() {
	defer safe.Recover()
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return
	}
	h.running = false
	h.stoppedAt = time.Now().UTC()
	if h.health.Status != HealthKilled && h.health.Status != HealthReleased {
		h.health.Status = HealthStopped
	}
	cancel := h.cancel
	done := h.done
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	slog.Info("hand stopped", "hand_id", h.id)
	h.emitEvent(natsapi.HelmEvent{Code: CodeHandStopped, Msg: "hand: stopped"})
}

// Kill stops the hand and immediately closes all open positions via market orders.
// Use for emergency shutdown when you must exit the exchange immediately.
func (h *Hand) Kill(ctx context.Context) {
	slog.Warn("hand: kill initiated — flattening all positions", "hand_id", h.id)
	h.emitEvent(natsapi.HelmEvent{Code: CodeHandKilled, Reason: "flattening all positions", Msg: "hand: killed"})
	h.mu.Lock()
	h.health.Status = HealthKilled
	h.mu.Unlock()
	h.flattenPositions(ctx)

	h.Stop()
	h.helmRuntime.RemoveHand(h.id.String())
}

// Release stops the hand without closing exchange-side positions.
// The hand performs a synthetic close (buyback) at market price so a trade record
// is logged, but no exchange order is placed. The physical position remains open
// on the exchange for the user to manage. Exchange-side SL/TP brackets are cancelled
// as safety cleanup.
func (h *Hand) Release(ctx context.Context) {
	slog.Info("hand: release — performing synthetic close (buyback)", "hand_id", h.id)
	h.emitEvent(natsapi.HelmEvent{Code: CodeHandReleased, Reason: "positions released (synthetic close)", Msg: "hand: released"})
	h.mu.Lock()
	h.health.Status = HealthReleased
	h.mu.Unlock()
	h.releasePositions(ctx)

	h.Stop()
	h.helmRuntime.RemoveHand(h.id.String())
}

// SetAllocatedCapital updates the hand's allocated capital budget dynamically.
func (h *Hand) SetAllocatedCapital(val decimal.Decimal) {
	h.mu.Lock()
	h.allocatedCap = val
	h.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Status checks — called by Registry and HelmRuntime
// ---------------------------------------------------------------------------

// IsRunning reports whether the run-loop goroutine is active.
func (h *Hand) IsRunning() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.running
}

// ---------------------------------------------------------------------------
// Queries — read-only snapshots for API responses
// ---------------------------------------------------------------------------

// Orders returns a snapshot of this hand's submitted orders.
func (h *Hand) Orders() []domain.Order {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]domain.Order, len(h.orders))
	copy(result, h.orders)
	return result
}

// Health returns a snapshot of the hand's health state.
func (h *Hand) Health() HandHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()
	health := h.health
	if h.running && h.health.StartedAt != nil {
		health.Uptime = time.Since(*h.health.StartedAt).Truncate(time.Second).String()
	}
	return health
}

// Position returns the current open position for this hand's symbol, or nil if flat.
func (h *Hand) Position() *portfolio.Position {
	return h.helmRuntime.Portfolio.GetPosition(h.Symbol)
}

// ---------------------------------------------------------------------------
// Inbox — called by Registry/Runtime to deliver external events
// ---------------------------------------------------------------------------

// DeliverSignal enqueues a signal onto the hand's signal channel (drain-replace).
// All signals including urgent exit signals go through the same channel for now.
func (h *Hand) DeliverSignal(sig Signal) {
	select {
	case <-h.Signals:
	default:
	}
	select {
	case h.Signals <- sig:
	default:
		h.RecordDrop()
		slog.Warn("hand signal channel full after drain, dropping",
			"hand_id", h.id, "symbol", sig.Symbol)
	}
}

// activeUnitCount returns the number of active position legs this hand currently holds.
// Thread-safe; called from HelmRuntime.OpenUnitCount.
func (h *Hand) activeUnitCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.pos.ActiveCount()
}

// MarkPartialApplied records the incremental qty, average price, and commission that the registry applied to the
// portfolio for a WS OrderEventPartialFill. This does NOT set seenFills — it
// intentionally allows the subsequent WS OrderEventFilled event to proceed through
// handleWsFill so that the final fill increment is applied and poslog is updated.
//
// The REST poll path (pollOrders "filled") deducts partialApplied from the cumulative
// FilledQty returned by GetOrder to avoid double-counting.
func (h *Hand) MarkPartialApplied(orderID string, qty, price, commission decimal.Decimal) {
	h.mu.Lock()
	state := h.partialApplied[orderID]
	state.Qty = state.Qty.Add(qty)
	state.Cost = state.Cost.Add(qty.Mul(price))
	state.Commission = state.Commission.Add(commission)
	h.partialApplied[orderID] = state
	h.mu.Unlock()
}

// EnqueueFill appends a fully-filled WsFillEvent to the hand's unbounded fill mailbox and
// wakes the run loop. Non-blocking and NEVER drops — a hand must always see its own fills
// (a dropped fill that falls back to a direct portfolio apply bypasses bracket/poslog/h.pos
// and silently desyncs the hand). Returns true unconditionally; the bool is kept so callers
// keep routing fills to the owning hand rather than the orphan path. Called by the registry
// fill processor (a different goroutine than the run loop).
func (h *Hand) EnqueueFill(ev exchange.WsFillEvent) bool {
	h.fillMu.Lock()
	h.fillQueue = append(h.fillQueue, ev)
	h.fillMu.Unlock()
	select {
	case h.fillSignal <- struct{}{}: // coalescing wakeup
	default: // a wakeup is already pending; the drain will see this fill too
	}
	return true
}

// drainFills applies every queued fill in arrival order. Runs ON the run loop (single-owner).
func (h *Hand) drainFills(ctx context.Context) {
	h.fillMu.Lock()
	batch := h.fillQueue
	h.fillQueue = nil
	h.fillMu.Unlock()
	for _, ev := range batch {
		h.handleWsFill(ctx, ev)
	}
}

// NotifyBracketPartialFill marks the SIBLING bracket order IDs as pending-cancel so
// HandleExitOrderCanceled treats the eventual sibling cancel as helm-initiated, not
// an external close.
//
// Problem this solves (Binance OCO / multi-leg TP fills):
//
//	When the TP leg of an OCO fills in multiple Binance executions, applyWsFill routes
//	each partial fill via MarkPartialApplied (no EnqueueFill → no cancelExitOrders).
//	Binance simultaneously auto-cancels the SL leg. If that cancel event arrives before
//	the FINAL TP fill is processed by applyFill (which is when cancelExitOrders runs),
//	HandleExitOrderCanceled finds the SL id NOT in pendingCancels → misidentified as
//	external close → EXT_CLOSE.
//
//	Calling this on the first partial fill pre-populates pendingCancels[sibling_id]
//	so the cancel event is always recognised as helm-initiated.
func (h *Hand) NotifyBracketPartialFill(orderID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, lv := range h.exitLevels {
		for _, id := range lv.ExchangeOrderIDs {
			if id == orderID {
				// Mark all OTHER bracket order IDs as pending helm-cancel.
				for _, sibID := range lv.ExchangeOrderIDs {
					if sibID != orderID {
						h.pendingCancels[sibID] = struct{}{}
					}
				}
				return
			}
		}
	}
}

// RecordDrop increments the dropped-signal counter. Called by the dispatcher.
func (h *Hand) RecordDrop() { h.metrics.signalsDropped.Add(1) }

// EnableEventSink initialises the test event bus. Call before Start().
// Multiple goroutines can call Subscribe() independently and each receives
// a full copy of every event (broadcast, not competing consumers).
func (h *Hand) EnableEventSink() {
	h.eventBus = &handEventBus{}
}

// Subscribe registers a new subscriber channel on the test event bus.
// Each subscriber receives its own copy of every event — safe for concurrent test helpers.
// Panics if EnableEventSink was not called first.
func (h *Hand) Subscribe(chanCap int) <-chan natsapi.HelmEvent {
	return h.eventBus.subscribe(chanCap)
}

// DeployedCapital returns the notional capital currently committed in open
// positions: sum(leg.Qty × leg.EntryPrice) across all active legs.
func (h *Hand) DeployedCapital() decimal.Decimal {
	h.mu.RLock()
	defer h.mu.RUnlock()
	total := decimal.Zero
	for _, leg := range h.pos.ActiveLegs() {
		total = total.Add(leg.Qty.Mul(leg.EntryPrice))
	}
	return total
}

// AllocatedCapital returns the hand's current allocated capital.
func (h *Hand) AllocatedCapital() decimal.Decimal {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.allocatedCap
}

// ActiveLegs returns a snapshot of all currently active position legs.
// Safe to call from any goroutine.
func (h *Hand) ActiveLegs() []domain.LegView {
	h.mu.RLock()
	legs := h.pos.ActiveLegs()
	h.mu.RUnlock()
	if len(legs) == 0 {
		return nil
	}
	out := make([]domain.LegView, 0, len(legs))
	for _, l := range legs {
		out = append(out, domain.LegView{
			PositionID:      l.PositionID,
			Symbol:          l.Symbol,
			Side:            l.Side,
			Phase:           string(l.Phase),
			Qty:             l.Qty,
			EntryPrice:      l.EntryPrice,
			DeployedCapital: l.DeployedCapital,
			StopLoss:        l.StopLoss,
			TakeProfit:      l.TakeProfit,
			OpenedAt:        l.OpenedAt,
		})
	}
	return out
}

// AvailableCash returns USDT liquid for this hand — not locked in open positions.
// = realizedEquity (allocated + cumPnL) - deployedCapital (entry cost of open legs).
func (h *Hand) AvailableCash() decimal.Decimal {
	avail := h.realizedEquity().Sub(h.DeployedCapital())
	if avail.IsNegative() {
		return decimal.Zero
	}
	return avail
}
