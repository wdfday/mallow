package runtime

import (
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/runtime/core/risk"
)

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Stop cleans up the circuit-breaker ticker and terminates the reset goroutine.
func (r *HelmRuntime) Stop() {
	if r.resetTicker != nil {
		r.resetTicker.Stop()
	}
	select {
	case <-r.stopCh: // already closed
	default:
		close(r.stopCh)
	}
}

// IsPaused reports whether the runtime is currently paused.
func (r *HelmRuntime) IsPaused() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.paused
}

// Pause suspends the runtime — all hands will ignore incoming signals.
// Returns IDs of hands that were running before the pause.
func (r *HelmRuntime) Pause() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = true
	var wasRunning []string
	for id, hand := range r.hands {
		hand.WasRunning = hand.IsRunning()
		if hand.WasRunning {
			wasRunning = append(wasRunning, id)
		}
	}
	r.EmitEvent(natsapi.HelmEvent{Code: CodeHelmPaused, Msg: "helm: paused"})
	return wasRunning
}

// Resume unpauses the runtime. Returns IDs of hands that should be restarted.
func (r *HelmRuntime) Resume() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = false
	var toRestart []string
	for id, hand := range r.hands {
		if hand.WasRunning {
			hand.WasRunning = false
			toRestart = append(toRestart, id)
		}
	}
	r.EmitEvent(natsapi.HelmEvent{Code: CodeHelmResumed, Msg: "helm: resumed"})
	return toRestart
}

// ---------------------------------------------------------------------------
// Risk config
// ---------------------------------------------------------------------------

// ResetHalt clears the risk-manager halt flag on this runtime.
func (r *HelmRuntime) ResetHalt() {
	r.RiskMgr.ResetHalt()
	r.EmitEvent(natsapi.HelmEvent{Code: CodeHelmUnhalted, Msg: "helm: halt reset"})
}

// UpdateRiskConfig replaces the live risk parameters.
func (r *HelmRuntime) UpdateRiskConfig(cfg risk.Config) {
	r.RiskMgr.UpdateConfig(cfg)
}
