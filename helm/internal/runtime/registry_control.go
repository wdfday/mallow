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
func (r *Registry) UpdateRiskConfig(id uuid.UUID, portfolio helmdomain.PortfolioConfig, riskCfg helmdomain.RiskConfig) error {
	r.mu.RLock()
	rt, ok := r.helmRuntimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil // not running; new config will be used on next Spawn
	}
	rt.UpdateRiskConfig(risk.Config{
		MaxPositions:      portfolio.MaxPositions,
		DailyLossLimitPct: riskCfg.DailyLossLimitPct,
		MaxDrawdownPct:    riskCfg.MaxDrawdownPct,
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
		slog.Warn("signal route: invalid helm_id", "helm_id", helmID, "hand_id", handID)
		return
	}
	r.mu.RLock()
	helmRuntime := r.helmRuntimes[id]
	r.mu.RUnlock()
	if helmRuntime == nil {
		slog.Warn("signal route: no helm found", "helm_id", helmID, "hand_id", handID)
		return
	}
	if !helmRuntime.DispatchHandSignal(handID, sig) {
		slog.Warn("signal route: hand not found in helm", "helm_id", helmID, "hand_id", handID)
	}
}
