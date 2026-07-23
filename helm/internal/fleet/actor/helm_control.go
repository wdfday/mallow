package actor

import (
	"mallow/helm/internal/fleet/actor/core/risk"
	"mallow/helm/internal/fleet/actor/eventcode"
	"mallow/helm/internal/infra/natsapi"
)

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Stop cleans up the circuit-breaker ticker and terminates the reset goroutine.
// Also cancels the fill-drain goroutines started by StartStreaming (if any).
func (r *HelmRuntime) Stop() {
	if r.resetTicker != nil {
		r.resetTicker.Stop()
	}
	select {
	case <-r.stopCh: // already closed
	default:
		close(r.stopCh)
	}
	r.fillStreamMu.Lock()
	if r.fillDrainCancel != nil {
		r.fillDrainCancel()
		r.fillDrainCancel = nil
	}
	r.fillStreamMu.Unlock()
}

// IsPaused reports whether the runtime is currently Paused.
func (r *HelmRuntime) IsPaused() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Paused
}

// Pause suspends the runtime — all hands will ignore incoming signals.
// Returns IDs of hands that were running before the pause.
func (r *HelmRuntime) Pause() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Paused = true
	var wasRunning []string
	for id, e := range r.hands {
		if e.h.IsRunning() {
			wasRunning = append(wasRunning, id)
		}
	}
	r.pausedHands = wasRunning
	r.EmitEvent(natsapi.HelmEvent{Code: eventcode.CodeHelmPaused, Msg: "helm: Paused"})
	return wasRunning
}

// Resume unpauses the runtime. Returns IDs of hands that should be restarted
// (those that were running when the helm was paused).
func (r *HelmRuntime) Resume() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Paused = false
	toRestart := r.pausedHands
	r.pausedHands = nil
	r.EmitEvent(natsapi.HelmEvent{Code: eventcode.CodeHelmResumed, Msg: "helm: resumed"})
	return toRestart
}

// ---------------------------------------------------------------------------
// Risk config
// ---------------------------------------------------------------------------

// ResetHalt clears the risk-manager halt flag on this runtime.
func (r *HelmRuntime) ResetHalt() {
	r.RiskMgr.ResetHalt()
	r.EmitEvent(natsapi.HelmEvent{Code: eventcode.CodeHelmUnhalted, Msg: "helm: halt reset"})
}

// UpdateRiskConfig replaces the live risk parameters.
func (r *HelmRuntime) UpdateRiskConfig(cfg risk.Config) {
	r.RiskMgr.UpdateConfig(cfg)
}
