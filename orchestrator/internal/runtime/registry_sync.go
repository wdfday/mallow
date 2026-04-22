package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"orchestrator/internal/infra/exchange"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/runtime/core/orderbook"
)

// persistSyncTime writes the runtime's lastSyncAt to the store (best-effort, logs on error).
func (r *Registry) persistSyncTime(rt *Orchestrator) {
	r.mu.RLock()
	ss := r.syncStore
	r.mu.RUnlock()
	if ss == nil {
		return
	}
	if err := ss.UpdateLastSyncedAt(rt.OrchestratorID, rt.LastSyncAt()); err != nil {
		slog.Warn("registry: persist last_synced_at failed", "orchestrator_id", rt.OrchestratorID, "err", err)
	}
}

// StartPollingSync starts a background goroutine that periodically syncs all runtimes
// whose exchange implements AccountSyncer. Used as fallback for disabled orchestrators
// (no WebSocket streaming) and as a catch-up for enabled ones.
// An immediate catch-up pass fires first (covers the gap since last_synced_at on respawn),
// then the periodic ticker takes over.
func (r *Registry) StartPollingSync(ctx context.Context, nc *nats.Conn, interval time.Duration) {
	syncAll := func() {
		r.mu.RLock()
		rts := make([]*Orchestrator, 0, len(r.runtimes))
		for _, rt := range r.runtimes {
			rts = append(rts, rt)
		}
		js := r.js
		r.mu.RUnlock()
		for _, rt := range rts {
			if err := rt.Sync(ctx, nc, js); err != nil {
				slog.Warn("registry: poll sync failed", "orchestrator_id", rt.OrchestratorID, "err", err)
			} else {
				r.persistSyncTime(rt)
			}
		}
	}

	go func() {
		// Immediate catch-up: cover the gap from last_synced_at → now on respawn.
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

// SpawnAll hydrates runtimes from a list of configs (called at startup).
// All non-halted configs are spawned; disabled ones get polling sync only (no streaming).
// Configs with status "paused" are spawned in paused state.
func (r *Registry) SpawnAll(cfgs []*orchdomain.OrchestratorConfig) {
	for _, cfg := range cfgs {
		if cfg.Status == "halted" {
			continue
		}
		if err := r.Spawn(cfg); err != nil {
			slog.Error("runtime: spawn failed", "orchestrator_id", cfg.ID, "err", err)
			continue
		}
		if cfg.Status == "paused" {
			if rt, err := r.Get(cfg.ID); err == nil {
				rt.Pause()
				slog.Info("runtime: spawned paused", "orchestrator_id", cfg.ID)
			}
		}
	}
}

// ReconcileAllOrders fetches open orders from the exchange for each runtime
// and re-tracks any that are missing from the in-memory orderbook.
// Call this after SpawnAll + StartFillStreaming so the fill processor is ready
// to handle fills that arrive for reconciled orders.
func (r *Registry) ReconcileAllOrders(ctx context.Context) {
	r.mu.RLock()
	rts := make([]*Orchestrator, 0, len(r.runtimes))
	for _, rt := range r.runtimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		r.reconcileOrders(ctx, rt)
	}
}

func (r *Registry) reconcileOrders(ctx context.Context, rt *Orchestrator) {
	reconciler, ok := rt.Exchange.(exchange.OrderReconciler)
	if !ok {
		return
	}
	// Fetch all open orders (symbol="" = all instruments).
	orders, err := reconciler.GetPendingOrders(ctx, rt.Creds, "")
	if err != nil {
		slog.Warn("reconcile orders: fetch failed",
			"orchestrator_id", rt.OrchestratorID, "err", err)
		return
	}
	if len(orders) == 0 {
		return
	}

	orchID := rt.OrchestratorID.String()
	// Build set of already-tracked order IDs so we don't double-track.
	tracked := make(map[string]struct{})
	for _, p := range rt.OrderBook.PendingOrders(orchID) {
		tracked[p.OrderID] = struct{}{}
	}

	recovered := 0
	for _, o := range orders {
		if _, exists := tracked[o.ID]; exists {
			continue
		}
		rt.OrderBook.TrackOrder(orderbook.PendingOrder{
			OrchestratorID: orchID,
			OrderID:        o.ID,
			BotID:          "", // unknown after crash
			Symbol:         o.Symbol,
			Side:           orderbook.OrderSide(o.Side),
		})
		recovered++
	}
	if recovered > 0 {
		slog.Info("reconcile orders: recovered pending orders",
			"orchestrator_id", rt.OrchestratorID,
			"recovered", recovered,
			"total_open", len(orders))
	}
}
