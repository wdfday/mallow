package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/natsapi"
	handdomain "mallow/helm/internal/module/hand/domain"
)

// HandHeartbeat is a lightweight snapshot of a hand for logging and monitoring.
type HandHeartbeat struct {
	ID           string
	Symbol       string
	Status       string
	StrategyName string
	Metrics      HandMetrics
}

// AddHand registers a hand and its persisted domain data with this runtime.
func (r *HelmRuntime) AddHand(hand *Hand, data *handdomain.Hand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hands[hand.id.String()] = &handEntry{h: hand, data: data}
}

// RemoveHand unregisters a hand from this runtime.
func (r *HelmRuntime) RemoveHand(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hands, id)
}

// HandIDs returns the IDs of all registered hands.
func (r *HelmRuntime) HandIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.hands))
	for id := range r.hands {
		ids = append(ids, id)
	}
	return ids
}

// RunningHandIDs returns the IDs of all currently running hands.
func (r *HelmRuntime) RunningHandIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ids []string
	for id, e := range r.hands {
		if e.h.IsRunning() {
			ids = append(ids, id)
		}
	}
	return ids
}

// HandSummaries returns a snapshot of all hands for heartbeat/debug logging.
func (r *HelmRuntime) HandSummaries() []HandHeartbeat {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HandHeartbeat, 0, len(r.hands))
	for _, e := range r.hands {
		out = append(out, HandHeartbeat{
			ID:           e.h.id.String(),
			Symbol:       e.h.Symbol,
			Status:       e.h.Health().Status,
			StrategyName: e.h.StrategyName,
			Metrics:      e.h.Metrics(),
		})
	}
	return out
}

// OpenUnitCount returns the total number of currently open position units across all hands.
func (r *HelmRuntime) OpenUnitCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, e := range r.hands {
		total += e.h.activeUnitCount()
	}
	return total
}

// DispatchHandSignal routes a signal to the named hand owned by this runtime.
// Returns false if the hand is not found.
func (r *HelmRuntime) DispatchHandSignal(handID string, sig Signal) bool {
	r.mu.RLock()
	e, ok := r.hands[handID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	if r.IsHalted() && !sig.IsUrgent() {
		r.EmitEvent(natsapi.HelmEvent{
			HandID:    handID,
			Code:      CodeSignalRejected,
			Symbol:    sig.Symbol,
			Direction: string(sig.Direction),
			Reason:    "helm halted",
			Msg:       "signal: skipped — helm halted",
		})
		return true
	}
	e.h.DeliverSignal(sig)
	return true
}

// GetHandEntry returns the live Hand runner and its persisted data for the given hand ID.
func (r *HelmRuntime) GetHandEntry(id string) (*Hand, *handdomain.Hand, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.hands[id]
	if !ok {
		return nil, nil, false
	}
	return e.h, e.data, true
}

// UpdateHandData applies fn to the persisted data of a live hand.
// Returns false if the hand is not found.
func (r *HelmRuntime) UpdateHandData(id string, fn func(*handdomain.Hand)) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.hands[id]
	if !ok {
		return false
	}
	fn(e.data)
	return true
}

// LiveHandSummaries returns domain summaries for all live (non-terminal) hands in this runtime.
func (r *HelmRuntime) LiveHandSummaries() []handdomain.HandSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]handdomain.HandSummary, 0, len(r.hands))
	for _, e := range r.hands {
		out = append(out, BuildHandSummary(e.h, e.data))
	}
	return out
}

// SymbolsByExchange returns deduplicated symbols across all live hands in this runtime,
// grouped by the runtime's exchange.
func (r *HelmRuntime) SymbolsByExchange() (exchange.Exchange, []string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]struct{})
	for _, e := range r.hands {
		for _, sym := range e.data.Symbols {
			if sym != "" {
				seen[sym] = struct{}{}
			}
		}
	}
	syms := make([]string, 0, len(seen))
	for sym := range seen {
		syms = append(syms, sym)
	}
	return r.Exchange, syms
}

// StartRunning starts the runner for every hand whose persisted status is HandStatusRunning.
// Called during service hydration after RegisterHandAll has already been called.
func (r *HelmRuntime) StartRunning() {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.hands {
		if e.data.Status == handdomain.HandStatusRunning {
			e.h.Start()
		}
	}
}

// StartHand registers the hand with herald and starts its runner.
// Persisted status update is the caller's responsibility.
func (r *HelmRuntime) StartHand(ctx context.Context, id string) error {
	r.mu.RLock()
	e, ok := r.hands[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("hand %q not found in runtime", id)
	}
	if e.data.Status.IsTerminal() {
		return fmt.Errorf("hand %q is %s — terminal hands cannot be restarted", id, e.data.Status)
	}
	if err := r.RegisterHandAll(ctx, id, r.HelmID.String(), e.data.Symbols, e.data.Strategy.Script, e.data.Strategy.Timeframe, e.data.Market == handdomain.MarketTypeFutures); err != nil {
		return fmt.Errorf("hand start: %w", err)
	}
	e.h.Start()
	// Keep the in-memory persisted status in sync so live summaries
	// (ListByHelmLive → BuildHandSummary) don't report a stale "stopped".
	r.mu.Lock()
	e.data.Status = handdomain.HandStatusRunning
	r.mu.Unlock()
	return nil
}

// ReregisterHand re-registers a hand with herald using its persisted config,
// without touching the runner state. Used on herald restart / heartbeat recovery.
// Returns false if the hand is not found or registration fails.
func (r *HelmRuntime) ReregisterHand(ctx context.Context, id string) bool {
	r.mu.RLock()
	e, ok := r.hands[id]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := r.RegisterHandAll(rctx, id, r.HelmID.String(), e.data.Symbols, e.data.Strategy.Script, e.data.Strategy.Timeframe, e.data.Market == handdomain.MarketTypeFutures); err != nil {
		slog.Error("herald re-register: failed", "hand_id", id, "err", err)
		return false
	}
	return true
}

// StopHand deregisters the hand from herald and stops its runner.
// Persisted status update is the caller's responsibility.
func (r *HelmRuntime) StopHand(ctx context.Context, id string) {
	r.mu.RLock()
	e, ok := r.hands[id]
	r.mu.RUnlock()
	if !ok {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	r.DeregisterHand(dctx, id)
	cancel()
	e.h.Stop()
	// Keep the in-memory persisted status in sync (see StartHand).
	r.mu.Lock()
	e.data.Status = handdomain.HandStatusStopped
	r.mu.Unlock()
}

// KillHand deregisters, removes, and kills the hand (flatten positions).
// Returns the final metrics snapshot. Persisted status update is the caller's responsibility.
func (r *HelmRuntime) KillHand(ctx context.Context, id string) (handdomain.HandMetricsView, bool) {
	r.mu.RLock()
	e, ok := r.hands[id]
	r.mu.RUnlock()
	if !ok {
		return handdomain.HandMetricsView{}, false
	}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	r.DeregisterHand(dctx, id)
	cancel()
	r.RemoveHand(id)
	e.h.Kill(ctx)
	return e.h.MetricsView(), true
}

// ReleaseHand deregisters, removes, and releases the hand (orphan positions).
// Returns the final metrics snapshot. Persisted status update is the caller's responsibility.
func (r *HelmRuntime) ReleaseHand(ctx context.Context, id string) (handdomain.HandMetricsView, bool) {
	r.mu.RLock()
	e, ok := r.hands[id]
	r.mu.RUnlock()
	if !ok {
		return handdomain.HandMetricsView{}, false
	}
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	r.DeregisterHand(dctx, id)
	cancel()
	r.RemoveHand(id)
	e.h.Release(ctx)
	return e.h.MetricsView(), true
}

// BuildHandSummary constructs a HandSummary from the live runner + persisted data.
func BuildHandSummary(h *Hand, data *handdomain.Hand) handdomain.HandSummary {
	health := h.Health()
	m := h.Metrics()

	hv := handdomain.HandHealthView{
		Status:    health.Status,
		LastError: health.LastError,
		Uptime:    health.Uptime,
	}
	if health.StartedAt != nil && !health.StartedAt.IsZero() {
		hv.StartedAt = health.StartedAt.Format(time.RFC3339)
	}
	if health.LastSignalAt != nil && !health.LastSignalAt.IsZero() {
		hv.LastSignalAt = health.LastSignalAt.Format(time.RFC3339)
	}
	if health.LastOrderAt != nil && !health.LastOrderAt.IsZero() {
		hv.LastOrderAt = health.LastOrderAt.Format(time.RFC3339)
	}
	if health.LastErrorAt != nil && !health.LastErrorAt.IsZero() {
		hv.LastErrorAt = health.LastErrorAt.Format(time.RFC3339)
	}

	return handdomain.HandSummary{
		ID:         data.ID,
		HelmID:     data.HelmID,
		Name:       data.Name,
		Type:       data.Type,
		Market:     data.Market,
		Symbols:    []string(data.Symbols),
		Strategy:   data.Strategy,
		Position:   data.Position,
		Guard:      data.Guard,
		Status:     data.Status,
		Running:    h.IsRunning(),
		OrderCount: len(h.Orders()),
		Health:     hv,
		Metrics: handdomain.HandMetricsView{
			SignalsReceived:   m.SignalsReceived,
			SignalsFiltered:   m.SignalsFiltered,
			SignalsDropped:    m.SignalsDropped,
			TradesApproved:    m.TradesApproved,
			OrdersPlaced:      m.OrdersPlaced,
			OrdersFilled:      m.OrdersFilled,
			OrdersFailed:      m.OrdersFailed,
			TotalPnL:          m.TotalPnL,
			TotalCommission:   m.TotalCommission,
			WinCount:          m.WinCount,
			LossCount:         m.LossCount,
			LatestSignalLagMs: m.LatestSignalLagMs,
			SignalQueueDepth:  m.SignalQueueDepth,
		},
		Futures:          data.Futures,
		CreatedAt:        data.CreatedAt,
		AllocatedCapital: data.AllocatedCapital,
		DeployedCapital:  h.DeployedCapital(),
		AvailableCash:    h.AvailableCash(),
		SignalTTLSec:     data.SignalTTLSec,
		Legs:             h.ActiveLegs(),
	}
}

// StopAllHands deregisters every hand from herald and stops its goroutine.
// Called during teardown — the runtime owns hand lifecycle, so tearing it down
// must cleanly stop the hands it holds.
func (r *HelmRuntime) StopAllHands(ctx context.Context) {
	r.mu.RLock()
	hands := make([]*Hand, 0, len(r.hands))
	ids := make([]string, 0, len(r.hands))
	for id, e := range r.hands {
		hands = append(hands, e.h)
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	for _, id := range ids {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		r.DeregisterHand(dctx, id)
		cancel()
	}
	for _, h := range hands {
		h.Stop()
	}
}

// AllOrders returns the live order list aggregated across all hands in this runtime.
func (r *HelmRuntime) AllOrders() []handdomain.Order {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []handdomain.Order
	for _, e := range r.hands {
		out = append(out, e.h.Orders()...)
	}
	return out
}

// SetAllocatedCapitalOnHand updates the in-memory allocated capital on the Hand runner.
func (r *HelmRuntime) SetAllocatedCapitalOnHand(id string, capital decimal.Decimal) {
	r.mu.RLock()
	e, ok := r.hands[id]
	r.mu.RUnlock()
	if ok {
		e.h.SetAllocatedCapital(capital)
	}
}
