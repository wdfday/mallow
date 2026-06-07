package runtime

import (
	"context"
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
// exchCfg is transient — sourced from the broker module, never persisted.
// Initial capital starts at zero and is updated on the first portfolio sync.
func (r *Registry) Spawn(cfg *helmdomain.Helm, exchCfg helmdomain.ExchangeConfig) error {
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

	pf := portfolio.New(decimal.Zero)
	riskCfg := risk.Config{
		MaxPositions:        cfg.Risk.MaxPositions,
		DailyLossLimitPct:   cfg.Risk.DailyLossLimitPct,
		MaxDrawdownPct:      cfg.Risk.MaxDrawdownPct,
		MaxGrossExposurePct: cfg.Risk.MaxGrossExposurePct,
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
	rt.FilterStore = r.market.filterViewFor(ex.Name())
	// Restore pause state so signal gating survives a restart.
	if cfg.Status == helmdomain.HelmStatusPaused || cfg.Status == helmdomain.HelmStatusHalted {
		rt.paused = true
	}

	// Wire the registry-owned per-exchange public data; all helms on the same
	// exchange share one bucket and see price + L2 updates immediately.
	rt.marketData = r.market.priceDataFor(ex.Name())

	// Wire the unit counter so MaxPositions counts actual open legs + manual positions,
	// not just distinct portfolio symbols.
	rt.RiskMgr.SetUnitCounter(rt.OpenUnitCount)
	// Wire the available-cash provider so the capital-adequacy gate (Gate 0.5) can
	// block new entries when totalCash drops below the sum of hand cash budgets.
	rt.RiskMgr.SetAvailableCashFn(rt.AvailableCash)

	r.mu.RLock()
	rt.PosLog = r.posLog
	rt.TradeLog = r.tradeLog
	rt.PnLSummer = r.pnlSummer
	rt.syncStore = r.syncStore
	rt.SetEventConn(r.nc, r.js)
	r.mu.RUnlock()

	// Register this runtime's price updater with the shared market streamer.
	r.mu.RLock()
	ms := r.marketStreamers[brokerType]
	r.mu.RUnlock()
	if ms != nil {
		// Registry-level UpdatePrice splits the herald "exchange:SYMBOL" prefix
		// and writes into the shared per-exchange price map. All helms on the same
		// exchange share that map, so only one handler registration per streamer is
		// needed — but AddPriceHandler is idempotent / deduplicated by the streamer.
		ms.AddPriceHandler(r.UpdatePrice)
	}

	r.mu.Lock()
	r.helmRuntimes[cfg.ID] = rt
	ctx := r.runCtx
	r.mu.Unlock()

	slog.Info("runtime: spawned", "helm_id", cfg.ID, "account_id", cfg.AccountID, "broker", brokerType)

	// If the app is already running (SetRuntime was called), start fill streaming immediately
	// so hot-plugged helms (from accountLinked events) get WS fills right away.
	if ctx != nil {
		rt.StartFillStreaming(ctx)
	}
	return nil
}

// RotateCreds updates the credentials of a running HelmRuntime in-place and
// reconnects the WS order stream. Running hands are not interrupted — REST calls
// (PlaceOrder, GetOrder) pick up new credentials immediately; the WS stream
// reconnects transparently. REST poll is the fill fallback during the brief gap.
func (r *Registry) RotateCreds(id uuid.UUID, newCreds exchange.Credentials) {
	r.mu.RLock()
	rt, ok := r.helmRuntimes[id]
	appCtx := r.runCtx
	r.mu.RUnlock()
	if !ok {
		slog.Warn("rotate creds: runtime not found", "helm_id", id)
		return
	}
	rt.RotateFillStream(appCtx, newCreds)
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

// StartFillStreaming starts account fill listeners for all runtimes whose exchange
// implements AccountStreamer. Called once from the app lifecycle after SetRuntime.
// Each HelmRuntime owns its own fill streaming goroutines — see helm_fills.go.
func (r *Registry) StartFillStreaming(ctx context.Context) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		rt.StartFillStreaming(ctx)
	}
}
