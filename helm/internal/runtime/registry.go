package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/poslog"
	orchdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/core/orderbook"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
)

// ExchangeFactory creates per-account exchange adapters from an ExchangeConfig.
type ExchangeFactory interface {
	New(cfg orchdomain.ExchangeConfig) (exchange.Exchange, error)
}

// MarketStreamerFactory creates shared market data streaming clients per broker type.
// Returns nil if the broker does not support market streaming.
type MarketStreamerFactory interface {
	New(cfg orchdomain.ExchangeConfig) exchange.MarketStreamer
}

// SyncStore persists the last successful sync timestamp for crash recovery.
// Implemented by domain.HelmRepo.
type SyncStore interface {
	UpdateLastSyncedAt(id uuid.UUID, t time.Time) error
}

// SignalSink is the narrow interface consumed by SignalDispatcher.
// Registry implements it; callers only see this interface.
type SignalSink interface {
	RouteSignal(orchID, botID string, sig Signal)
}

// Registry manages all live Orchestrator instances.
// One Orchestrator per active orchestrator config.
// OrderBooks and MarketStreamers are shared per broker type.
type Registry struct {
	mu              sync.RWMutex
	runtimes        map[uuid.UUID]*HelmRuntime
	orderBooks      map[string]orderbook.OrderBook     // broker_type → shared OrderBook
	marketStreamers map[string]exchange.MarketStreamer // broker_type → shared streamer

	exchFactory     ExchangeFactory
	streamerFactory MarketStreamerFactory

	// nc, js, and runCtx are set once via SetRuntime after startup.
	nc        *nats.Conn
	js        nats.JetStreamContext
	runCtx    context.Context
	syncStore SyncStore
	posLog    poslog.Log // nil when NATS unavailable
}

// NewRegistry creates an empty Registry.
func NewRegistry(factory ExchangeFactory, streamerFactory MarketStreamerFactory) *Registry {
	return &Registry{
		runtimes:        make(map[uuid.UUID]*HelmRuntime),
		orderBooks:      make(map[string]orderbook.OrderBook),
		marketStreamers: make(map[string]exchange.MarketStreamer),
		exchFactory:     factory,
		streamerFactory: streamerFactory,
	}
}

// SetSyncStore injects the persistence port for last-sync timestamps (breaks init cycle).
func (r *Registry) SetSyncStore(store SyncStore) {
	r.mu.Lock()
	r.syncStore = store
	r.mu.Unlock()
}

// SetPosLog injects the position event log (breaks init cycle — called after NATS connects).
func (r *Registry) SetPosLog(log poslog.Log) {
	r.mu.Lock()
	r.posLog = log
	r.mu.Unlock()
}

// SetRuntime stores the NATS connection, JetStream context, and run context.
// Called from the app lifecycle OnStart, after the connection is established.
// js is cached here so fill processors never call nc.JetStream() per event.
func (r *Registry) SetRuntime(ctx context.Context, nc *nats.Conn) {
	if ctx == nil {
		ctx = context.Background()
	}
	js, err := nc.JetStream()
	if err != nil {
		slog.Error("registry: failed to obtain JetStream context", "err", err)
	}
	r.mu.Lock()
	r.nc = nc
	r.js = js
	r.runCtx = ctx
	r.mu.Unlock()
}

// OrderBook returns the shared OrderBook for a given broker type.
func (r *Registry) OrderBook(brokerType string) orderbook.OrderBook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.orderBooks[brokerType]
}

// Spawn creates and registers an OrchestratorRuntime for the given config.
func (r *Registry) Spawn(cfg *orchdomain.HelmConfig) error {
	ex, err := r.exchFactory.New(cfg.Exchange)
	if err != nil {
		return fmt.Errorf("registry: create exchange for %q: %w", cfg.ID, err)
	}

	pf := portfolio.New(decimal.NewFromFloat(cfg.Capital))
	riskCfg := risk.Config{
		// Sizing limits from PortfolioConfig
		MaxPositions:   cfg.Portfolio.MaxPositions,
		MaxPositionPct: cfg.Portfolio.MaxPositionPct,
		// Circuit-breakers from RiskConfig
		DailyLossLimitPct: cfg.Risk.DailyLossLimitPct,
		MaxDrawdownPct:    cfg.Risk.MaxDrawdownPct,
	}
	riskMgr := risk.New(riskCfg, pf)

	r.mu.Lock()
	ob, ok := r.orderBooks[cfg.Exchange.BrokerType]
	if !ok {
		ob = orderbook.NewOrderBook(cfg.Exchange.BrokerType)
		r.orderBooks[cfg.Exchange.BrokerType] = ob
		slog.Info("runtime: orderbook created", "broker", cfg.Exchange.BrokerType)
	}
	if _, ok := r.marketStreamers[cfg.Exchange.BrokerType]; !ok {
		if r.streamerFactory != nil {
			if ms := r.streamerFactory.New(cfg.Exchange); ms != nil {
				r.marketStreamers[cfg.Exchange.BrokerType] = ms
				slog.Info("runtime: market streamer created", "broker", cfg.Exchange.BrokerType)
			}
		}
	}
	r.mu.Unlock()

	creds := exchange.Credentials{
		APIKey:     cfg.Exchange.APIKey,
		APISecret:  cfg.Exchange.APISecret,
		Passphrase: cfg.Exchange.Passphrase,
		AccountID:  cfg.Exchange.AccountID,
	}
	rt := NewHelmRuntime(cfg.ID, cfg.AccountID, cfg.UserID, cfg.Exchange.BrokerType, pf, riskMgr, ob, ex, creds, cfg.LastSyncedAt)
	// Restore pause state so signal gating survives a restart.
	if cfg.Status == "paused" || cfg.Status == "halted" {
		rt.paused = true
	}

	r.mu.RLock()
	rt.PosLog = r.posLog
	r.mu.RUnlock()

	// Register this runtime's price updater with the shared market streamer.
	r.mu.RLock()
	ms := r.marketStreamers[cfg.Exchange.BrokerType]
	r.mu.RUnlock()
	if ms != nil {
		ms.AddPriceHandler(rt.UpdatePrice)
		if bs, ok := ms.(exchange.BookStreamer); ok {
			bs.AddBookHandler(rt.UpdateL2)
			slog.Info("runtime: L2 book streaming registered", "helm_id", cfg.ID, "broker", cfg.Exchange.BrokerType)
		}
	}

	r.mu.Lock()
	r.runtimes[cfg.ID] = rt
	ctx := r.runCtx
	nc := r.nc
	r.mu.Unlock()

	slog.Info("runtime: spawned", "helm_id", cfg.ID, "account_id", cfg.AccountID, "broker", cfg.Exchange.BrokerType)

	// If the app is already running (SetRuntime was called), start fill streaming immediately
	// so hot-plugged orchestrators (from accountLinked events) get WS fills right away.
	if ctx != nil && nc != nil {
		r.startFillStream(ctx, nc, rt)
	}
	return nil
}

// Teardown stops and removes the OrchestratorRuntime for the given orchestrator.
// Returns hand IDs that were registered so the caller can stop them.
func (r *Registry) Teardown(id uuid.UUID) []string {
	r.mu.Lock()
	rt, ok := r.runtimes[id]
	var botIDs []string
	if ok {
		botIDs = rt.BotIDs()
		rt.Stop()
		delete(r.runtimes, id)
	}
	r.mu.Unlock()

	if ok {
		slog.Info("runtime: torn down", "helm_id", id, "bots_orphaned", len(botIDs))
	}
	return botIDs
}

// Pause pauses a runtime: signals rejected, returns hand IDs that were running.
func (r *Registry) Pause(id uuid.UUID) ([]string, error) {
	r.mu.RLock()
	rt, ok := r.runtimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for orchestrator %q", id)
	}

	wasRunning := rt.Pause()
	slog.Info("runtime: paused", "helm_id", id, "bots_stopped", len(wasRunning))
	return wasRunning, nil
}

// Resume resumes a paused runtime: returns hand IDs that should be restarted.
func (r *Registry) Resume(id uuid.UUID) ([]string, error) {
	r.mu.RLock()
	rt, ok := r.runtimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for orchestrator %q", id)
	}

	toRestart := rt.Resume()
	slog.Info("runtime: resumed", "helm_id", id, "bots_restarting", len(toRestart))
	return toRestart, nil
}

// UpdateRiskConfig updates the live risk parameters for the given orchestrator.
// No-op if the runtime is not active (e.g. halted/disabled).
func (r *Registry) UpdateRiskConfig(id uuid.UUID, portfolio orchdomain.PortfolioConfig, riskCfg orchdomain.RiskConfig) error {
	r.mu.RLock()
	rt, ok := r.runtimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil // not running; new config will be used on next Spawn
	}
	rt.UpdateRiskConfig(risk.Config{
		MaxPositions:      portfolio.MaxPositions,
		MaxPositionPct:    portfolio.MaxPositionPct,
		DailyLossLimitPct: riskCfg.DailyLossLimitPct,
		MaxDrawdownPct:    riskCfg.MaxDrawdownPct,
	})
	return nil
}

// ResetHalt clears the risk-manager halt flag for the given orchestrator.
func (r *Registry) ResetHalt(id uuid.UUID) error {
	r.mu.RLock()
	rt, ok := r.runtimes[id]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("registry: no runtime for orchestrator %q", id)
	}
	rt.ResetHalt()
	slog.Info("runtime: halt reset", "helm_id", id)
	return nil
}

// Get returns the OrchestratorRuntime for the given orchestrator ID.
func (r *Registry) Get(id uuid.UUID) (*HelmRuntime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.runtimes[id]
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for orchestrator %q", id)
	}
	return rt, nil
}

// All returns all active runtimes.
func (r *Registry) All() []*HelmRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*HelmRuntime, 0, len(r.runtimes))
	for _, rt := range r.runtimes {
		out = append(out, rt)
	}
	return out
}

// RouteSignal implements SignalSink. Routes a signal directly to the target orchestrator
// (looked up by orchID) which then dispatches it to the target bot.
// Called from SignalDispatcher in the NATS callback goroutine — must be non-blocking.
func (r *Registry) RouteSignal(orchID, botID string, sig Signal) {
	id, err := uuid.Parse(orchID)
	if err != nil {
		slog.Warn("signal route: invalid orch_id", "orch_id", orchID, "hand_id", botID)
		return
	}
	r.mu.RLock()
	orch := r.runtimes[id]
	r.mu.RUnlock()
	if orch == nil {
		slog.Warn("signal route: no orchestrator found", "orch_id", orchID, "hand_id", botID)
		return
	}
	if !orch.DispatchHandSignal(botID, sig) {
		slog.Warn("signal route: hand not found in orchestrator", "orch_id", orchID, "hand_id", botID)
	}
}

// SyncOne satisfies RuntimeSpawner — triggers an async one-shot sync for id.
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
