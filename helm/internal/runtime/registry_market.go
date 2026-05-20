package runtime

import (
	"mallow/helm/internal/infra/exchange"
)

// LatestL2 returns the most recent L2 snapshot for a symbol from the given broker.
// ok=false if no snapshot has been received yet.
func (r *Registry) LatestL2(brokerType, symbol string) (exchange.L2Snapshot, bool) {
	r.l2Mu.RLock()
	defer r.l2Mu.RUnlock()
	if m, ok := r.l2Books[brokerType]; ok {
		if snap, ok := m[symbol]; ok {
			return snap, true
		}
	}
	return exchange.L2Snapshot{}, false
}

// handleL2 returns the L2 book handler for a given broker type.
// Registered once per market streamer; all helms of the same broker share this cache
// via the getL2 closure injected at Spawn() — no per-helm copy or fan-out needed.
func (r *Registry) handleL2(brokerType string) func(exchange.L2Snapshot) {
	return func(snap exchange.L2Snapshot) {
		r.l2Mu.Lock()
		m := r.l2Books[brokerType]
		if m == nil {
			m = make(map[string]exchange.L2Snapshot)
			r.l2Books[brokerType] = m
		}
		m[snap.Symbol] = snap
		r.l2Mu.Unlock()
	}
}
