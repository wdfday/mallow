package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"mallow/helm/internal/fleet/actor"
	signalfollower "mallow/helm/internal/fleet/actor/signal-follower"
	"mallow/helm/internal/fleet/dispatcher"
	"mallow/helm/internal/fleet/market"
	"mallow/helm/internal/fleet/perf"
	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/journal/poslog"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// ExchangeFactory creates per-account exchange adapters from an ExchangeConfig.
// Public market data streaming (prices, filters, L2) is handled separately by
// the fleet/market package — it is not the factory's responsibility.
type ExchangeFactory interface {
	New(cfg helmdomain.ExchangeConfig) (exchange.Exchange, error)
}

// Registry manages all live Helm instances.
// One Helm per active helm config.
type Registry struct {
	// ── Live runtimes ─────────────────────────────────────────────────────
	mu           sync.RWMutex
	helmRuntimes map[uuid.UUID]*actor.HelmRuntime

	// ── Per-exchange market data caches ───────────────────────────────────
	// market self-connects to each exchange's public WebSocket and is the
	// sole writer of price/filter/L2 data, keyed by exchange name.  Each
	// actor.HelmRuntime gets a scoped view wired at Spawn time (see
	// registry_lifecycle.go's use of FilterViewFor / PriceDataFor).
	market *market.MarketContext

	// ── Factory (injected at construction) ────────────────────────────────
	exchFactory ExchangeFactory

	// ── Runtime wiring (set via SetRuntime / Set* after startup) ──────────
	nc           *nats.Conn
	js           nats.JetStreamContext
	runCtx       context.Context
	syncStore    actor.SyncStore
	posLog       poslog.Log                      // nil when NATS unavailable
	tradeLog     perf.TradeLog                   // JetStream HELM_TRADES — drained into PG by TradePersister
	pnlSummer    signalfollower.HandPnLSummer    // postgres aggregate querier for RestorePnL + RestoreGuard
	eventCounter signalfollower.HandEventCounter // postgres event-count aggregate for RestoreCounters

	// ── Signal routing ────────────────────────────────────────────────────
	dispatcher *dispatcher.SignalDispatcher // wired via SetDispatcher after startup; nil before
	metrics    registryMetrics              // routing miss counters, exported via DispatchStats

	// herald is shared across all HelmRuntimes for hand register/deregister.
	herald actor.HeraldRegistrar

	// accountEvents is set once at startup via SetAccountEventHandler.
	// Propagated to each runtime at Spawn; nil = no-op.
	accountEvents actor.AccountEventHandler
}

// NewRegistry creates an empty Registry.
func NewRegistry(factory ExchangeFactory) *Registry {
	return &Registry{
		helmRuntimes: make(map[uuid.UUID]*actor.HelmRuntime),
		market:       market.NewMarketContext(),
		exchFactory:  factory,
	}
}

// SetHerald injects the herald registrar (infra/herald.Client).
// Propagated to all already-spawned runtimes so helms hydrated before
// NATS connects (the normal startup order) still get the client.
func (r *Registry) SetHerald(h actor.HeraldRegistrar) {
	r.mu.Lock()
	r.herald = h
	for _, rt := range r.helmRuntimes {
		rt.Herald = h
	}
	r.mu.Unlock()
}

// SetSyncStore injects the persistence port for last-sync timestamps (breaks init cycle).
// Propagated to all already-spawned runtimes so hydrated helms persist sync times immediately.
func (r *Registry) SetSyncStore(store actor.SyncStore) {
	r.mu.Lock()
	r.syncStore = store
	for _, rt := range r.helmRuntimes {
		rt.SyncStore = store
	}
	r.mu.Unlock()
}

// SetPosLog injects the position event log (breaks init cycle — called after NATS connects).
func (r *Registry) SetPosLog(log poslog.Log) {
	r.mu.Lock()
	r.posLog = log
	for _, rt := range r.helmRuntimes {
		rt.PosLog = log
	}
	r.mu.Unlock()
}

// SetAccountEventHandler wires the outbound port for account/connection-level
// conditions (credential rejection, sustained network/exchange errors, margin
// calls, trading restrictions — see actor.AccountEventHandler). Called once at
// startup from the broker module so it can persist connection status changes.
// Propagated to all already-spawned runtimes, same as SetHerald/SetSyncStore/SetPosLog.
func (r *Registry) SetAccountEventHandler(h actor.AccountEventHandler) {
	r.mu.Lock()
	r.accountEvents = h
	for _, rt := range r.helmRuntimes {
		rt.AccountEvents = h
	}
	r.mu.Unlock()
}

// SetTradeLog injects the JetStream trade log. Propagated to all already-spawned runtimes.
// Persistence to PostgreSQL is handled out-of-band by TradePersister.
func (r *Registry) SetTradeLog(log perf.TradeLog) {
	r.mu.Lock()
	r.tradeLog = log
	for _, rt := range r.helmRuntimes {
		rt.TradeLog = log
	}
	r.mu.Unlock()
}

// SetPnLSummer injects the postgres PnL aggregate querier. Propagated to all runtimes.
// Replaces the JetStream full-drain in RestorePnL with a single SQL query on startup.
func (r *Registry) SetPnLSummer(ps signalfollower.HandPnLSummer) {
	r.mu.Lock()
	r.pnlSummer = ps
	for _, rt := range r.helmRuntimes {
		rt.PnLSummer = ps
	}
	r.mu.Unlock()
}

// SetEventCounter injects the postgres event-count aggregate querier. Propagated to
// all runtimes so RestoreCounters can rebuild activity counters on startup.
func (r *Registry) SetEventCounter(ec signalfollower.HandEventCounter) {
	r.mu.Lock()
	r.eventCounter = ec
	for _, rt := range r.helmRuntimes {
		rt.EventCounter = ec
	}
	r.mu.Unlock()
}

// SetRuntime stores the NATS connection, JetStream context, and run context.
// Called from the app lifecycle OnStart, after the connection is established.
// js is cached here so fill processors never call nc.JetStream() per event.
// Also propagates nc/js to all already-spawned runtimes (hydrated before startup)
// so their EmitEvent calls publish to NATS instead of slog-only.
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
	for _, rt := range r.helmRuntimes {
		rt.SetEventConn(nc, js)
	}
	r.mu.Unlock()
}

// Get returns the actor.HelmRuntime for the given helm ID.
func (r *Registry) Get(id uuid.UUID) (*actor.HelmRuntime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.helmRuntimes[id]
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for helm %q", id)
	}
	return rt, nil
}

// All returns all active runtimes.
func (r *Registry) All() []*actor.HelmRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*actor.HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		out = append(out, rt)
	}
	return out
}

// PrewarmFilters delegates to the market cache — see market.MarketContext.PrewarmFilters.
func (r *Registry) PrewarmFilters(ctx context.Context, symbolsByExchange map[exchange.Exchange][]string) {
	r.market.PrewarmFilters(ctx, symbolsByExchange)
}

// StartMarketStreaming delegates to the market cache — see market.MarketContext.StartStreaming.
// Self-connects to each exchange present in symbolsByExchange and becomes the
// sole writer of that exchange's price/L2 data; safe to call again as new
// exchanges appear.
func (r *Registry) StartMarketStreaming(ctx context.Context, symbolsByExchange map[exchange.Exchange][]string) {
	r.market.StartStreaming(ctx, symbolsByExchange)
}

// ── registryMetrics ───────────────────────────────────────────────────────────

// registryMetrics holds all runtime counters for the Registry.
type registryMetrics struct {
	routeNoHelm int64 // helm_id not found in registry    (atomic)
	routeNoHand int64 // hand_id not found in actor.HelmRuntime (atomic)
}

func (m *registryMetrics) incNoHelm() { atomic.AddInt64(&m.routeNoHelm, 1) }
func (m *registryMetrics) incNoHand() { atomic.AddInt64(&m.routeNoHand, 1) }

func (m *registryMetrics) loadNoHelm() int64 { return atomic.LoadInt64(&m.routeNoHelm) }
func (m *registryMetrics) loadNoHand() int64 { return atomic.LoadInt64(&m.routeNoHand) }
