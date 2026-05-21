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
)

// ---------------------------------------------------------------------------
// Lifecycle — called by Registry on behalf of user actions
// ---------------------------------------------------------------------------

// Start spawns the bot's run-loop goroutine.
func (h *Hand) Start() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return
	}
	h.running = true
	h.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	h.cancel = cancel
	h.health.Status = HealthRunning
	h.health.StartedAt = new(time.Now().UTC())
	go h.run(ctx)
	slog.Info("hand started", "hand_id", h.id, "exchange", h.helmRuntime.Exchange.Name())
	h.helmRuntime.EmitEvent(natsapi.HelmEvent{HandID: h.id.String(), Code: CodeHandStarted, Msg: "hand: started"})
	h.activityLog.push(ActivityEntry{At: time.Now(), Code: CodeHandStarted, Symbol: h.Symbol, Reason: "hand run-loop started"})
}

// Stop cancels the run-loop and waits for it to exit.
func (h *Hand) Stop() {
	h.mu.Lock()
	if !h.running {
		h.mu.Unlock()
		return
	}
	h.running = false
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
	h.helmRuntime.EmitEvent(natsapi.HelmEvent{HandID: h.id.String(), Code: CodeHandStopped, Msg: "hand: stopped"})
	h.activityLog.push(ActivityEntry{At: time.Now(), Code: CodeHandStopped, Symbol: h.Symbol, Reason: "hand run-loop stopped"})
}

// Pause suspends signal processing without stopping the run-loop goroutine.
// In-flight polls and fills continue to run; new signals are dropped.
func (h *Hand) Pause() {
	h.mu.Lock()
	h.paused = true
	h.health.Status = HealthPaused
	h.mu.Unlock()
	slog.Info("hand paused", "hand_id", h.id)
	h.helmRuntime.EmitEvent(natsapi.HelmEvent{HandID: h.id.String(), Code: CodeHandPaused, Msg: "hand: paused"})
	h.activityLog.push(ActivityEntry{At: time.Now(), Code: CodeHandPaused, Symbol: h.Symbol, Reason: "hand manually paused via API"})
}

// Resume re-enables signal processing after a Pause.
func (h *Hand) Resume() {
	h.mu.Lock()
	if h.running {
		h.paused = false
		h.health.Status = HealthRunning
	}
	h.mu.Unlock()
	slog.Info("hand resumed", "hand_id", h.id)
	h.helmRuntime.EmitEvent(natsapi.HelmEvent{HandID: h.id.String(), Code: CodeHandResumed, Msg: "hand: resumed"})
	h.activityLog.push(ActivityEntry{At: time.Now(), Code: CodeHandResumed, Symbol: h.Symbol, Reason: "hand manually resumed via API"})
}

// Kill stops the hand and immediately closes all open positions via market orders.
// Use for emergency shutdown when you must exit the exchange immediately.
func (h *Hand) Kill(ctx context.Context) {
	slog.Warn("hand: kill initiated — flattening all positions", "hand_id", h.id)
	h.helmRuntime.EmitEvent(natsapi.HelmEvent{HandID: h.id.String(), Code: CodeHandKilled, Reason: "flattening all positions", Msg: "hand: killed"})
	h.activityLog.push(ActivityEntry{At: time.Now(), Code: CodeHandKilled, Symbol: h.Symbol, Reason: "hand killed — flattening positions"})
	h.mu.Lock()
	h.paused = true
	h.health.Status = HealthKilled
	h.mu.Unlock()
	h.flattenPositions(ctx)

	// Publish final flat snapshot after kill
	if h.helmRuntime.SnapshotLog != nil {
		snap := h.handSnapshot(time.Now().UTC())
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := h.helmRuntime.SnapshotLog.Append(sctx, snap); err != nil {
			slog.Warn("snapshot_log: hand snapshot append failed on kill", "hand_id", h.id, "err", err)
		}
		cancel()
	}

	h.Stop()
	h.helmRuntime.RemoveHand(h.id.String())
}

// Release stops the hand without closing open positions.
// Each open leg is emitted as KindPositionOrphaned so the reconciler never
// reclaims it on restart. The position stays live at the exchange with any
// exchange-side SL/TP already placed.
func (h *Hand) Release(ctx context.Context) {
	slog.Info("hand: release — orphaning open positions", "hand_id", h.id)
	h.helmRuntime.EmitEvent(natsapi.HelmEvent{HandID: h.id.String(), Code: CodeHandReleased, Reason: "positions orphaned at exchange", Msg: "hand: released"})
	h.activityLog.push(ActivityEntry{At: time.Now(), Code: CodeHandReleased, Symbol: h.Symbol, Reason: "hand released — positions orphaned"})
	h.mu.Lock()
	h.paused = true
	h.health.Status = HealthReleased
	h.mu.Unlock()
	h.releasePositions(ctx)

	// Publish final flat snapshot after release
	if h.helmRuntime.SnapshotLog != nil {
		snap := h.handSnapshot(time.Now().UTC())
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := h.helmRuntime.SnapshotLog.Append(sctx, snap); err != nil {
			slog.Warn("snapshot_log: hand snapshot append failed on release", "hand_id", h.id, "err", err)
		}
		cancel()
	}

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

// IsPaused reports whether the hand is individually paused.
func (h *Hand) IsPaused() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.paused
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

// Metrics returns a snapshot of the hand's trading counters and P&L.
func (h *Hand) Metrics() HandMetrics {
	h.metrics.mu.Lock()
	defer h.metrics.mu.Unlock()
	return HandMetrics{
		SignalsReceived: h.metrics.signalsReceived.Load(),
		SignalsFiltered: h.metrics.signalsFiltered.Load(),
		SignalsDropped:  h.metrics.signalsDropped.Load(),
		TradesApproved:  h.metrics.tradesApproved.Load(),
		OrdersPlaced:    h.metrics.ordersPlaced.Load(),
		OrdersFilled:    h.metrics.ordersFilled.Load(),
		OrdersFailed:    h.metrics.ordersFailed.Load(),
		TotalPnL:        h.metrics.totalPnL,
		WinCount:        h.metrics.winCount,
		LossCount:       h.metrics.lossCount,
	}
}

// Position returns the current open position for this hand's symbol, or nil if flat.
func (h *Hand) Position() *portfolio.Position {
	return h.helmRuntime.Portfolio.GetPosition(h.Symbol)
}

// L2 returns the latest L2 order-book snapshot for this hand's symbol.
// ok=false if no snapshot has been received yet (e.g. market streamer not connected).
func (h *Hand) L2() (exchange.L2Snapshot, bool) {
	return h.helmRuntime.LatestL2(h.Symbol)
}

// ---------------------------------------------------------------------------
// Inbox — called by Registry/Runtime to deliver external events
// ---------------------------------------------------------------------------

// DeliverSignal enqueues a signal onto the appropriate hand channel.
// Urgent signals (close/exit) are buffered; regular signals drain-replace
// so the hand always sees the latest, never a stale one.
func (h *Hand) DeliverSignal(sig Signal) {
	if sig.IsUrgent() {
		select {
		case h.UrgentSignals <- sig:
		default:
			slog.Error("hand urgent signal channel full, dropping close signal",
				"hand_id", h.id, "symbol", sig.Symbol)
		}
		return
	}

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

// MarkFillSeen records an orderID in seenFills without processing the fill.
// Called by the registry when a partial fill is applied directly to the portfolio
// (bypassing the hand run-loop), so pollOrders does not double-apply the same fill.
func (h *Hand) MarkFillSeen(orderID string) {
	h.mu.Lock()
	h.seenFills[orderID] = struct{}{}
	h.mu.Unlock()
}

// EnqueueFill forwards a fully-filled WS OrderEvent to the hand's run-loop.
// Non-blocking: if the buffer is full, the fill will be picked up by the REST
// poll fallback instead. Called by the registry fill processor.
func (h *Hand) EnqueueFill(ev exchange.OrderEvent) {
	select {
	case h.fillCh <- ev:
	default:
		slog.Warn("hand: fill channel full, REST poll will handle",
			"hand_id", h.id, "order_id", ev.OrderID)
	}
}

// RecordDrop increments the dropped-signal counter. Called by the dispatcher.
func (h *Hand) RecordDrop() { h.metrics.signalsDropped.Add(1) }

// recordActivity pushes an entry into the in-memory activity ring.
// The ring holds the last activityRingSize events for test observability and
// the recent-activity UI chip; durable history lives in HELM_EVENTS JetStream.
func (h *Hand) recordActivity(e ActivityEntry) { h.activityLog.push(e) }

// Activity returns a snapshot of recent activity entries, oldest-first.
// The ring holds at most activityRingSize (20) entries; older events are evicted.
func (h *Hand) Activity() []ActivityEntry { return h.activityLog.Snapshot() }

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
