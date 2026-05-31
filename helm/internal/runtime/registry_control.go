package runtime

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/risk"
)

// ---------------------------------------------------------------------------
// Helm-level control — called by the service layer on behalf of user actions
// ---------------------------------------------------------------------------

// Pause pauses a helm: signals rejected, returns hand IDs that were running.
func (r *Registry) Pause(id uuid.UUID) ([]string, error) {
	r.mu.RLock()
	rt, ok := r.helmRuntimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for helm %q", id)
	}
	wasRunning := rt.Pause()
	slog.Info("helm: paused", "helm_id", id, "hands_stopped", len(wasRunning))
	return wasRunning, nil
}

// Resume resumes a paused runtime: returns hand IDs that should be restarted.
func (r *Registry) Resume(id uuid.UUID) ([]string, error) {
	r.mu.RLock()
	rt, ok := r.helmRuntimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for helm %q", id)
	}
	toRestart := rt.Resume()
	slog.Info("helm: resumed", "helm_id", id, "hands_restarting", len(toRestart))
	return toRestart, nil
}

// UpdateRiskConfig updates the live risk parameters for the given helm.
// No-op if the runtime is not active (e.g. halted/disabled).
func (r *Registry) UpdateRiskConfig(id uuid.UUID, riskCfg helmdomain.RiskConfig) error {
	r.mu.RLock()
	rt, ok := r.helmRuntimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil // not running; new config will be used on next Spawn
	}
	rt.UpdateRiskConfig(risk.Config{
		MaxPositions:        riskCfg.MaxPositions,
		DailyLossLimitPct:   riskCfg.DailyLossLimitPct,
		MaxDrawdownPct:      riskCfg.MaxDrawdownPct,
		MaxGrossExposurePct: riskCfg.MaxGrossExposurePct,
	})
	return nil
}

// ResetHalt clears the risk-manager halt flag for the given helm.
func (r *Registry) ResetHalt(id uuid.UUID) error {
	r.mu.RLock()
	rt, ok := r.helmRuntimes[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("registry: no runtime for helm %q", id)
	}
	rt.ResetHalt()
	slog.Info("helm: halt reset", "helm_id", id)
	return nil
}

// ---------------------------------------------------------------------------
// Signal routing
// ---------------------------------------------------------------------------

// RouteSignal implements SignalSink. Routes a signal directly to the target helm
// (looked up by helmID) which then dispatches it to the target hand.
// Called from SignalDispatcher in the NATS callback goroutine — must be non-blocking.
func (r *Registry) RouteSignal(helmID, handID string, sig Signal) {
	id, err := uuid.Parse(helmID)
	if err != nil {
		r.routeNoHelm.Add(1)
		slog.Warn("signal route: invalid helm_id", "helm_id", helmID, "hand_id", handID)
		return
	}
	r.mu.RLock()
	helmRuntime := r.helmRuntimes[id]
	r.mu.RUnlock()
	if helmRuntime == nil {
		r.routeNoHelm.Add(1)
		slog.Warn("signal route: no helm found", "helm_id", helmID, "hand_id", handID)
		return
	}
	if !helmRuntime.DispatchHandSignal(handID, sig) {
		r.routeNoHand.Add(1)
		slog.Warn("signal route: hand not found in helm", "helm_id", helmID, "hand_id", handID)
	}
}

// DispatchStats is a point-in-time snapshot of registry-level signal routing errors.
type DispatchStats struct {
	RouteNoHelm int64 // signals where helm_id was not found (invalid UUID or unknown helm)
	RouteNoHand int64 // signals where hand_id was not found in the resolved HelmRuntime
}

// DispatchStats returns a snapshot of routing error counters.
func (r *Registry) DispatchStats() DispatchStats {
	return DispatchStats{
		RouteNoHelm: r.routeNoHelm.Load(),
		RouteNoHand: r.routeNoHand.Load(),
	}
}

// SetDispatcher wires the SignalDispatcher so its NATS-level counters can be
// exported via NATSStats. Called from the app lifecycle after startup.
func (r *Registry) SetDispatcher(d *SignalDispatcher) {
	r.mu.Lock()
	r.dispatcher = d
	r.mu.Unlock()
}

// DispatcherSnapshot is a serializable point-in-time copy of DispatcherMetrics.
// Uses plain int64 so the struct is safe to copy and return by value.
type DispatcherSnapshot struct {
	SignalsTotal      int64 `json:"signals_total"`
	SignalsMissingID  int64 `json:"signals_missing_id"`
	SignalsNilPayload int64 `json:"signals_nil_payload"`
	SignalsDispatched int64 `json:"signals_dispatched"`
}

// NATSStats returns a snapshot of the NATS-level dispatcher counters.
// Returns zeros if no dispatcher has been wired yet.
func (r *Registry) NATSStats() DispatcherSnapshot {
	r.mu.RLock()
	d := r.dispatcher
	r.mu.RUnlock()
	if d == nil {
		return DispatcherSnapshot{}
	}
	return DispatcherSnapshot{
		SignalsTotal:      d.Metrics.SignalsTotal.Load(),
		SignalsMissingID:  d.Metrics.SignalsMissingID.Load(),
		SignalsNilPayload: d.Metrics.SignalsNilPayload.Load(),
		SignalsDispatched: d.Metrics.SignalsDispatched.Load(),
	}
}
