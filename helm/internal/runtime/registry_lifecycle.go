package runtime

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
)

// Spawn creates and registers a HelmRuntime for the given config.
// exchCfg and capital are transient — sourced from broker module in helm, never persisted.
func (r *Registry) Spawn(cfg *helmdomain.Helm, exchCfg helmdomain.ExchangeConfig, capital decimal.Decimal) error {
	r.mu.RLock()
	_, exists := r.helmRuntimes[cfg.ID]
	r.mu.RUnlock()
	if exists {
		slog.Debug("runtime: already spawned, skipping", "helm_id", cfg.ID)
		return nil
	}

	ex, err := r.exchFactory.New(exchCfg)
	if err != nil {
		return fmt.Errorf("registry: create exchange for %q: %w", cfg.ID, err)
	}

	pf := portfolio.New(capital)
	riskCfg := risk.Config{
		MaxPositions:      cfg.Portfolio.MaxPositions,
		DailyLossLimitPct: cfg.Risk.DailyLossLimitPct,
		MaxDrawdownPct:    cfg.Risk.MaxDrawdownPct,
	}
	riskMgr := risk.New(riskCfg, pf)

	brokerType := exchCfg.BrokerType
	r.mu.Lock()
	if _, ok := r.marketStreamers[brokerType]; !ok {
		if r.streamerFactory != nil {
			if ms := r.streamerFactory.New(exchCfg); ms != nil {
				r.marketStreamers[brokerType] = ms
				slog.Info("runtime: market streamer created", "broker", brokerType)
				// Register the L2 book handler once per streamer.
				// A single handler fans-out to all helms of this broker type —
				// subsequent helms do not need their own registration.
				if bs, ok := ms.(exchange.BookStreamer); ok {
					bs.AddBookHandler(r.handleL2(brokerType))
					slog.Info("runtime: L2 book handler registered", "broker", brokerType)
				}
			}
		}
	}
	r.mu.Unlock()

	creds := exchange.Credentials{
		APIKey:     exchCfg.APIKey,
		APISecret:  exchCfg.APISecret,
		Passphrase: exchCfg.Passphrase,
		AccountID:  exchCfg.AccountID,
	}
	rt := NewHelmRuntime(cfg.ID, cfg.AccountID, cfg.UserID, brokerType, pf, riskMgr, ex, creds, cfg.LastSyncedAt, cfg.CreatedAt)
	// Restore pause state so signal gating survives a restart.
	if cfg.Status == helmdomain.HelmStatusPaused || cfg.Status == helmdomain.HelmStatusHalted {
		rt.paused = true
	}

	// Delegate L2 lookups to the registry's shared broker-level cache.
	// Captured by value so the closure stays valid after Spawn returns.
	rt.getL2 = func(symbol string) (exchange.L2Snapshot, bool) {
		return r.LatestL2(brokerType, symbol)
	}

	// Wire the unit counter so MaxPositions counts actual open legs + manual positions,
	// not just distinct portfolio symbols.
	rt.RiskMgr.SetUnitCounter(rt.OpenUnitCount)

	r.mu.RLock()
	rt.PosLog = r.posLog
	rt.SnapshotLog = r.snapshotLog
	rt.TradeLog = r.tradeLog
	rt.SetEventConn(r.nc, r.js)
	r.mu.RUnlock()

	// Register this runtime's price updater with the shared market streamer.
	r.mu.RLock()
	ms := r.marketStreamers[brokerType]
	r.mu.RUnlock()
	if ms != nil {
		// Price is still per-helm: each account has its own portfolio that tracks
		// unrealized P&L from price ticks.
		ms.AddPriceHandler(rt.UpdatePrice)
	}

	r.mu.Lock()
	r.helmRuntimes[cfg.ID] = rt
	ctx := r.runCtx
	nc := r.nc
	r.mu.Unlock()

	slog.Info("runtime: spawned", "helm_id", cfg.ID, "account_id", cfg.AccountID, "broker", brokerType)

	// If the app is already running (SetRuntime was called), start fill streaming immediately
	// so hot-plugged helms (from accountLinked events) get WS fills right away.
	if ctx != nil && nc != nil {
		r.startFillStream(ctx, nc, rt)
	}
	return nil
}

// Teardown stops and removes the HelmRuntime for the given helm.
// Returns hand IDs that were registered so the caller can stop them.
func (r *Registry) Teardown(id uuid.UUID) []string {
	r.mu.Lock()
	rt, ok := r.helmRuntimes[id]
	var handIDs []string
	if ok {
		handIDs = rt.HandIDs()
		rt.Stop()
		delete(r.helmRuntimes, id)
	}
	r.mu.Unlock()

	if ok {
		slog.Info("runtime: torn down", "helm_id", id, "hands_orphaned", len(handIDs))
	}
	return handIDs
}
