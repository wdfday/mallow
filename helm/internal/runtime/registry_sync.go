package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/exchange"
)

// persistSyncTime writes the runtime's lastSyncAt to the store (best-effort, logs on error).
func (r *Registry) persistSyncTime(rt *HelmRuntime) {
	r.mu.RLock()
	ss := r.syncStore
	r.mu.RUnlock()
	if ss == nil {
		return
	}
	if err := ss.UpdateLastSyncedAt(rt.HelmID, rt.LastSyncAt()); err != nil {
		slog.Warn("registry: persist last_synced_at failed", "helm_id", rt.HelmID, "err", err)
	}
}

// StartPollingSync starts a background goroutine that periodically syncs all runtimes
// whose exchange implements AccountSyncer. Used as fallback for disabled helms
// (no WebSocket streaming) and as a catch-up for enabled ones.
// An immediate catch-up pass fires first (covers the gap since last_synced_at on respawn),
// then the periodic ticker takes over.
func (r *Registry) StartPollingSync(ctx context.Context, nc *nats.Conn, interval time.Duration) {
	syncAll := func() {
		r.mu.RLock()
		rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
		for _, rt := range r.helmRuntimes {
			rts = append(rts, rt)
		}
		js := r.js
		r.mu.RUnlock()
		for _, rt := range rts {
			if err := rt.Sync(ctx, nc, js); err != nil {
				slog.Warn("registry: poll sync failed", "helm_id", rt.HelmID, "err", err)
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

// ReconcileAllOrders fetches open orders from the exchange for each runtime
// and re-tracks any that are missing from the in-memory orderbook.
// Call this after SpawnAll + StartFillStreaming so the fill processor is ready
// to handle fills that arrive for reconciled orders.
func (r *Registry) ReconcileAllOrders(ctx context.Context) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		r.reconcileOrders(ctx, rt)
	}
}

// RecoverGapFills fetches filled order history from each exchange that implements
// HistoryFetcher, covering the window [lastSyncAt, now). Fills that were missed
// while helm was offline are applied to the portfolio and published to TRADE_FILLS
// JetStream. Call this after ReconcileAllOrders but before starting WS streaming
// so the fill processor is ready to receive.
func (r *Registry) RecoverGapFills(ctx context.Context, nc *nats.Conn) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	js := r.js
	r.mu.RUnlock()
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}

	for _, rt := range rts {
		historian, ok := rt.Exchange.(exchange.HistoryFetcher)
		if !ok {
			continue
		}
		lastSync := rt.LastSyncAt()
		if lastSync.IsZero() {
			continue // never synced — no gap to recover
		}
		now := time.Now().UTC()
		if now.Sub(lastSync) < 5*time.Second {
			continue // restarted too recently, nothing to recover
		}

		// Pass nil symbols — fetch all instruments. Symbol hints are no longer available
		// from the tracking map (orderID→handID only) without a separate symbol index.
		var symbols []string

		slog.Info("gap recovery: fetching fill history",
			"helm_id", rt.HelmID, "from", lastSync, "to", now, "symbols", len(symbols))

		fills, err := historian.FilledOrders(ctx, rt.Creds, symbols, lastSync, now)
		if err != nil {
			slog.Error("gap recovery: fetch failed", "helm_id", rt.HelmID, "err", err)
			continue
		}
		if len(fills) == 0 {
			slog.Info("gap recovery: no fills in gap", "helm_id", rt.HelmID)
			continue
		}

		applied := 0
		for _, txn := range fills {
			// Dedup: skip if this trade was already processed (poslog has it).
			if rt.HasProcessedTrade(txn.TradeID) {
				continue
			}
			ev := exchange.OrderEvent{
				Type:      exchange.OrderEventFilled,
				OrderID:   txn.OrderID,
				TradeID:   txn.TradeID,
				Symbol:    txn.Symbol,
				Side:      exchange.OrderSide(txn.Side),
				FilledQty: txn.Qty,
				FilledAvg: txn.AvgPrice,
				Timestamp: txn.FilledAt,
			}
			r.applyFill(nc, js, rt, ev)
			applied++
		}
		slog.Info("gap recovery: fills applied",
			"helm_id", rt.HelmID, "total", len(fills), "applied", applied, "skipped", len(fills)-applied)
	}
}

// SyncOne satisfies RuntimeSpawner — triggers an async one-shot sync for the given helm.
func (r *Registry) SyncOne(id uuid.UUID) {
	r.mu.RLock()
	ctx := r.runCtx
	nc := r.nc
	js := r.js
	r.mu.RUnlock()
	if ctx == nil {
		ctx = context.Background()
	}
	rt, err := r.Get(id)
	if err != nil {
		return
	}
	go func() {
		if err := rt.Sync(ctx, nc, js); err != nil {
			slog.Warn("registry: sync failed", "helm_id", id, "err", err)
			return
		}
		r.persistSyncTime(rt)
	}()
}

func (r *Registry) reconcileOrders(ctx context.Context, rt *HelmRuntime) {
	reconciler, ok := rt.Exchange.(exchange.OrderReconciler)
	if !ok {
		return
	}
	// Fetch all open orders (symbol="" = all instruments).
	orders, err := reconciler.GetPendingOrders(ctx, rt.Creds, "")
	if err != nil {
		slog.Warn("reconcile orders: fetch failed",
			"helm_id", rt.HelmID, "err", err)
		return
	}
	if len(orders) == 0 {
		return
	}

	recovered := 0
	for _, o := range orders {
		// Skip orders already tracked (hand placed them before reconcile ran).
		if rt.HasOrderTracking(o.ID) {
			continue
		}
		// handID unknown after crash — fill routing will fall back to REST poll.
		rt.TrackOrder(o.ID, "")
		recovered++
	}
	if recovered > 0 {
		slog.Info("reconcile orders: recovered pending orders",
			"helm_id", rt.HelmID,
			"recovered", recovered,
			"total_open", len(orders))
	}
}
