package runtime

import "log/slog"

// HandSummary is a lightweight snapshot of a hand for logging and monitoring.
type HandSummary struct {
	ID      string
	Symbol  string
	Status  string
	Metrics HandMetrics
}

// AddHand registers a hand with this runtime.
func (r *HelmRuntime) AddHand(hand *Hand) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bots[hand.id.String()] = hand
}

// RemoveHand unregisters a hand from this runtime.
func (r *HelmRuntime) RemoveHand(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bots, id)
}

// BotIDs returns the IDs of all registered hands.
func (r *HelmRuntime) BotIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.bots))
	for id := range r.bots {
		ids = append(ids, id)
	}
	return ids
}

// RunningBotIDs returns the IDs of all currently running hands.
func (r *HelmRuntime) RunningBotIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ids []string
	for id, hand := range r.bots {
		if hand.IsRunning() {
			ids = append(ids, id)
		}
	}
	return ids
}

// HandSummaries returns a snapshot of all hands for heartbeat/debug logging.
func (r *HelmRuntime) HandSummaries() []HandSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HandSummary, 0, len(r.bots))
	for _, h := range r.bots {
		out = append(out, HandSummary{
			ID:      h.id.String(),
			Symbol:  h.Symbol,
			Status:  h.Health().Status,
			Metrics: h.Metrics(),
		})
	}
	return out
}

// DispatchHandSignal routes a signal to the named hand owned by this runtime.
// Returns false if the hand is not found.
func (r *HelmRuntime) DispatchHandSignal(handID string, sig Signal) bool {
	r.mu.RLock()
	hand, ok := r.bots[handID]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	if hand.IsPaused() {
		slog.Debug("runtime: hand paused, signal skipped",
			"hand_id", handID, "symbol", sig.Symbol, "direction", sig.Direction)
		return true
	}
	hand.DeliverSignal(sig)
	return true
}
