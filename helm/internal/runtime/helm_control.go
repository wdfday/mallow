package runtime

import (
	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/runtime/core/risk"
)

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Stop cleans up the circuit-breaker ticker and terminates the reset goroutine.
// Also cancels the fill-drain goroutines started by StartFillStreaming (if any).
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
	for id, e := range r.hands {
		if e.h.IsRunning() {
			wasRunning = append(wasRunning, id)
		}
	}
	r.pausedHands = wasRunning
	r.EmitEvent(natsapi.HelmEvent{Code: CodeHelmPaused, Msg: "helm: paused"})
	return wasRunning
}

// Resume unpauses the runtime. Returns IDs of hands that should be restarted
// (those that were running when the helm was paused).
func (r *HelmRuntime) Resume() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = false
	toRestart := r.pausedHands
	r.pausedHands = nil
	r.EmitEvent(natsapi.HelmEvent{Code: CodeHelmResumed, Msg: "helm: resumed"})
	return toRestart
}

// ---------------------------------------------------------------------------
// Risk config
// ---------------------------------------------------------------------------

// TriggerAuthError is called when an exchange auth error (ErrClassAuth) is detected
// mid-run. It self-pauses the helm, emits a credential-error event, and notifies the
// broker service via the onCredentialError callback (set at spawn time) so the broker
// connection can be marked as error in the DB. Safe to call from any goroutine.
// The callback runs in a new goroutine to avoid blocking the caller.
func (r *HelmRuntime) TriggerAuthError(reason string) {
	r.Pause()
	r.EmitEvent(natsapi.HelmEvent{Code: CodeHelmCredentialError, Msg: reason})
	if r.onCredentialError != nil {
		go r.onCredentialError(r.AccountID, reason)
	}
}

// ResetHalt clears the risk-manager halt flag on this runtime.
func (r *HelmRuntime) ResetHalt() {
	r.RiskMgr.ResetHalt()
	r.EmitEvent(natsapi.HelmEvent{Code: CodeHelmUnhalted, Msg: "helm: halt reset"})
}

// UpdateRiskConfig replaces the live risk parameters.
func (r *HelmRuntime) UpdateRiskConfig(cfg risk.Config) {
	r.RiskMgr.UpdateConfig(cfg)
}
