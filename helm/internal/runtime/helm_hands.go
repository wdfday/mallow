package runtime

import (
	"mallow/helm/internal/infra/natsapi"
)

// HandHeartbeat is a lightweight snapshot of a hand for logging and monitoring.
type HandHeartbeat struct {
	ID           string
	Symbol       string
	Status       string
	StrategyName string
	Metrics      HandMetrics
}

// AddHand registers a hand with this runtime.
func (r *HelmRuntime) AddHand(hand *Hand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hands[hand.id.String()] = hand
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
	for id, hand := range r.hands {
		if hand.IsRunning() {
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
	for _, h := range r.hands {
		out = append(out, HandHeartbeat{
			ID:           h.id.String(),
			Symbol:       h.Symbol,
			Status:       h.Health().Status,
			StrategyName: h.StrategyName,
			Metrics:      h.Metrics(),
		})
	}
	return out
}

// OpenUnitCount returns the total number of currently open position units
// across all hands (sum of active legs). Manual portfolio positions are excluded —
// MaxPositions caps bot-managed units only.
// Used by the risk Manager's MaxPositions gate.
func (r *HelmRuntime) OpenUnitCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, h := range r.hands {
		total += h.activeUnitCount()
	}
	return total
}

// DispatchHandSignal routes a signal to the named hand owned by this runtime.
// Returns false if the hand is not found.
func (r *HelmRuntime) DispatchHandSignal(handID string, sig Signal) bool {
	r.mu.RLock()
	hand, ok := r.hands[handID]
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
	hand.DeliverSignal(sig)
	return true
}
