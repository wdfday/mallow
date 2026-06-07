package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/safe"
)

// StartPollingSync starts a background goroutine that periodically syncs all runtimes
// whose exchange implements AccountSyncer. An immediate catch-up pass fires first
// (covers the gap since last_synced_at on respawn), then a ticker takes over.
func (r *Registry) StartPollingSync(ctx context.Context, interval time.Duration) {
	syncAll := func() {
		r.mu.RLock()
		rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
		for _, rt := range r.helmRuntimes {
			rts = append(rts, rt)
		}
		r.mu.RUnlock()
		for _, rt := range rts {
			if err := rt.Sync(ctx); err != nil {
				slog.Warn("registry: poll sync failed", "helm_id", rt.HelmID, "err", err)
			} else {
				rt.persistSyncTime()
			}
		}
	}

	go func() {
		defer safe.Recover()
		slog.Info("registry: startup sync pass running")
		syncAll()
		slog.Info("registry: startup sync pass done")

		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				syncAll()
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("registry: polling sync started", "interval", interval)
}

// ReconcileAllOrders re-tracks open orders for all runtimes after restart.
// Call after SpawnAll + StartFillStreaming so the fill processor is ready.
func (r *Registry) ReconcileAllOrders(ctx context.Context) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		rt.ReconcileOrders(ctx)
	}
}

// RecoverGapFills applies fills missed during downtime for all runtimes.
// Call after ReconcileAllOrders but before StartFillStreaming.
func (r *Registry) RecoverGapFills(ctx context.Context) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		rt.RecoverGapFills(ctx)
	}
}

// RecoverAllBrackets re-places missing exchange OCO bracket orders for every hand
// across all runtimes. Call AFTER RecoverGapFills so gap fills are applied first
// (prevents re-placing a bracket for a position that filled during downtime).
func (r *Registry) RecoverAllBrackets(ctx context.Context) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		rt.RecoverHandBrackets(ctx)
	}
}

// SyncOne triggers an async one-shot sync for the given helm.
func (r *Registry) SyncOne(id uuid.UUID) {
	r.mu.RLock()
	ctx := r.runCtx
	r.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	rt, err := r.Get(id)
	if err != nil {
		return
	}
	go func() {
		defer safe.Recover()
		if err := rt.Sync(ctx); err != nil {
			slog.Warn("registry: sync failed", "helm_id", id, "err", err)
			return
		}
		rt.persistSyncTime()
	}()
}
