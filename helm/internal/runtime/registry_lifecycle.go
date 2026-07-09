package runtime

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
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
	creds := exchange.Credentials{
		APIKey:     exchCfg.APIKey,
		APISecret:  exchCfg.APISecret,
		Passphrase: exchCfg.Passphrase,
		AccountID:  exchCfg.AccountID,
	}
	rt := NewHelmRuntime(cfg.ID, cfg.AccountID, cfg.UserID, brokerType, pf, riskMgr, ex, creds, cfg.LastSyncedAt, cfg.CreatedAt)
	rt.Herald = r.herald
	rt.FilterStore = r.market.filterViewFor(ex.Name())
	// Restore pause state so signal gating survives a restart.
	// HelmStatusError also starts paused — credentials must be rotated before resuming.
	if cfg.Status == helmdomain.HelmStatusPaused ||
		cfg.Status == helmdomain.HelmStatusHalted ||
		cfg.Status == helmdomain.HelmStatusError {
		rt.paused = true
	}
	// Wire credential-error callback so auth failures mid-run propagate to the broker service.
	r.mu.RLock()
	rt.onCredentialError = r.onCredentialError
	r.mu.RUnlock()

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
	rt.EventCounter = r.eventCounter
	rt.syncStore = r.syncStore
	rt.SetEventConn(r.nc, r.js)
	r.mu.RUnlock()

	r.mu.Lock()
	r.helmRuntimes[cfg.ID] = rt
	ctx := r.runCtx
	r.mu.Unlock()

	slog.Info("runtime: spawned", "helm_id", cfg.ID, "account_id", cfg.AccountID, "broker", brokerType)

	// If the app is already running (SetRuntime was called), start fill streaming immediately
	// so hot-plugged helms (from accountLinked events) get WS fills right away.
	//
	// Skip for HelmStatusError: those credentials are already known bad (that's how the
	// helm got here). Reconnecting on every restart just re-runs the same doomed WS login,
	// re-fires TriggerAuthError → MarkCredentialError, and spams reconnect-loop logs forever
	// until the user rotates the key. RotateCredsForAccount → RotateStream reconnects
	// explicitly with the new credentials once that happens, independent of this path.
	if ctx != nil && cfg.Status != helmdomain.HelmStatusError {
		rt.StartStreaming(ctx)
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
	rt.RotateStream(appCtx, newCreds)
}

// Teardown stops and removes the HelmRuntime for the given helm.
// Returns hand IDs that were registered so the caller can stop them.
func (r *Registry) Teardown(id uuid.UUID) []string {
	r.mu.Lock()
	rt, ok := r.helmRuntimes[id]
	var handIDs []string
	if ok {
		handIDs = rt.HandIDs()
		delete(r.helmRuntimes, id)
	}
	r.mu.Unlock()

	if ok {
		// The runtime owns its hands — stop and deregister them before shutting down.
		rt.StopAllHands(context.Background())
		rt.Stop()
		slog.Info("runtime: torn down", "helm_id", id, "hands_stopped", len(handIDs))
	}
	return handIDs
}

// PurgeHelmData removes JetStream messages scoped to this helm and account.
// Called during hard-delete (broker connection removal) so no audit data lingers.
// Errors are logged and ignored — purge is best-effort.
func (r *Registry) PurgeHelmData(helmID, accountID uuid.UUID) {
	if r.js == nil {
		return
	}
	hid := helmID.String()
	aid := accountID.String()
	purges := []struct {
		stream  string
		subject string
	}{
		{"HELM_POSITIONS", "helm.pos." + hid + ".>"},
		{"HELM_TRADES", "helm.trades." + hid + ".>"},
		{"HELM_EVENTS", "helm.events." + hid},
		{"TRADE_FILLS", "trade.filled." + aid},
		{"PORTFOLIO_SYNC", "portfolio.synced." + aid},
	}
	for _, p := range purges {
		if err := r.js.PurgeStream(p.stream, &nats.StreamPurgeRequest{Subject: p.subject}); err != nil {
			slog.Warn("registry: PurgeHelmData: stream purge failed (non-fatal)",
				"stream", p.stream, "subject", p.subject, "err", err)
		}
	}
	slog.Info("registry: helm JetStream data purged", "helm_id", hid, "account_id", aid)
}

// StartStreaming starts account fill listeners for all runtimes whose exchange
// implements AccountStreamer. Called once from the app lifecycle after SetRuntime.
// Each HelmRuntime owns its own fill streaming goroutines — see helm_fills.go.
func (r *Registry) StartStreaming(ctx context.Context) {
	r.mu.RLock()
	rts := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		rt.StartStreaming(ctx)
	}
}
