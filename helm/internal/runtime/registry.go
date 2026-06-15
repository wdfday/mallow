package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/poslog"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/perf"
	"mallow/helm/internal/safe"
)

// ExchangeFactory creates per-account exchange adapters from an ExchangeConfig.
// Public market data streaming (prices, L2) is handled separately in lifecycle.go
// via buildMarketDataListener — it is not the factory's responsibility.
type ExchangeFactory interface {
	New(cfg helmdomain.ExchangeConfig) (exchange.Exchange, error)
}

// SyncStore persists the last successful sync timestamp for crash recovery.
// Implemented by domain.HelmRepo.
type SyncStore interface {
	UpdateLastSyncedAt(id uuid.UUID, t time.Time) error
}

// SignalSink is the narrow interface consumed by SignalDispatcher.
// Registry implements it; callers only see this interface.
type SignalSink interface {
	RouteSignal(helmID, handID string, sig Signal)
}

// Registry manages all live Helm instances.
// One Helm per active helm config.
// MarketStreamers are stored inside exchangePublicData alongside the price/filter/l2
// caches they feed — both are keyed by exchange name (= broker type), so there is
// no need for a separate marketStreamers map.
type Registry struct {
	// ── Live runtimes ─────────────────────────────────────────────────────
	mu           sync.RWMutex
	helmRuntimes map[uuid.UUID]*HelmRuntime

	// ── Per-exchange market data caches ───────────────────────────────────
	// market groups symbolFilters, prices, l2Books, and the market streamer
	// under one struct per exchange name.  Each HelmRuntime gets a scoped
	// view wired at Spawn time (see filterViewFor / priceViewFor).
	market exchangeMarketCache

	// ── Factory (injected at construction) ────────────────────────────────
	exchFactory ExchangeFactory

	// ── Runtime wiring (set via SetRuntime / Set* after startup) ──────────
	nc        *nats.Conn
	js        nats.JetStreamContext
	runCtx    context.Context
	syncStore SyncStore
	posLog    poslog.Log    // nil when NATS unavailable
	tradeLog  perf.TradeLog // JetStream HELM_TRADES — drained into PG by TradePersister
	pnlSummer HandPnLSummer // postgres aggregate querier for RestorePnL

	// ── Signal routing ────────────────────────────────────────────────────
	dispatcher *SignalDispatcher // wired via SetDispatcher after startup; nil before
	metrics    registryMetrics   // routing miss counters, exported via DispatchStats

	// onCredentialError is set once at startup via SetCredentialErrorHook.
	// Propagated to each runtime at Spawn; nil = no-op.
	onCredentialError func(accountID uuid.UUID, reason string)
}

// NewRegistry creates an empty Registry.
func NewRegistry(factory ExchangeFactory) *Registry {
	return &Registry{
		helmRuntimes: make(map[uuid.UUID]*HelmRuntime),
		market:       newExchangeMarketCache(),
		exchFactory:  factory,
	}
}

// SetSyncStore injects the persistence port for last-sync timestamps (breaks init cycle).
// Propagated to all already-spawned runtimes so hydrated helms persist sync times immediately.
func (r *Registry) SetSyncStore(store SyncStore) {
	r.mu.Lock()
	r.syncStore = store
	for _, rt := range r.helmRuntimes {
		rt.syncStore = store
	}
	r.mu.Unlock()
}

// SetPosLog injects the position event log (breaks init cycle — called after NATS connects).
func (r *Registry) SetPosLog(log poslog.Log) {
	r.mu.Lock()
	r.posLog = log
	r.mu.Unlock()
}

// SetCredentialErrorHook wires a callback that fires when a running HelmRuntime detects
// an exchange auth error (ErrClassAuth from PlaceOrder or WS). Called once at startup
// from the broker module so it can persist the connection status change.
func (r *Registry) SetCredentialErrorHook(fn func(accountID uuid.UUID, reason string)) {
	r.mu.Lock()
	r.onCredentialError = fn
	r.mu.Unlock()
}

// DirtyRuntimes returns runtimes that have been marked dirty since the last call.
// Atomically claims (clears) the dirty flag, so each fill burst is flushed once.
// Implements perflog.SnapshotSource.
func (r *Registry) DirtyRuntimes() []perflog.SnapshotEmitter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []perflog.SnapshotEmitter
	for _, rt := range r.helmRuntimes {
		if rt.snapshotDirty.CompareAndSwap(1, 0) {
			out = append(out, rt)
		}
	}
	return out
}

// AllRuntimes returns all live runtimes for the unconditional heartbeat sweep.
// Implements perflog.SnapshotSource.
func (r *Registry) AllRuntimes() []perflog.SnapshotEmitter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]perflog.SnapshotEmitter, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		out = append(out, rt)
	}
	return out
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
func (r *Registry) SetPnLSummer(ps HandPnLSummer) {
	r.mu.Lock()
	r.pnlSummer = ps
	for _, rt := range r.helmRuntimes {
		rt.PnLSummer = ps
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

// Get returns the HelmRuntime for the given helm ID.
func (r *Registry) Get(id uuid.UUID) (*HelmRuntime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.helmRuntimes[id]
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for helm %q", id)
	}
	return rt, nil
}

// All returns all active runtimes.
func (r *Registry) All() []*HelmRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*HelmRuntime, 0, len(r.helmRuntimes))
	for _, rt := range r.helmRuntimes {
		out = append(out, rt)
	}
	return out
}

// UpdatePrice is the registry-level price handler registered with each market streamer.
// herald attaches an "exchange:" prefix to symbol names (e.g. "binance:ETHUSDT").
// UpdatePrice splits on ":" to obtain the exchange name and bare symbol, then writes
// into the correct per-exchange price map. All HelmRuntimes wired to that exchange
// share the same map and therefore see the update immediately.
func (r *Registry) UpdatePrice(heraldSym string, price decimal.Decimal) {
	if !price.IsPositive() {
		return
	}
	exchangeName, bareSym, ok := strings.Cut(heraldSym, ":")
	if !ok {
		// No prefix — treat the whole string as a bare symbol on an unnamed exchange.
		bareSym = heraldSym
		exchangeName = ""
	}
	r.market.updatePrice(exchangeName, bareSym, price)
}

// PrewarmFilters fetches symbol filters for (exchange, symbol) pairs owned by
// active hands. symbolsByExchange must be obtained from the hand service so that
// only pairs that actually belong to each exchange are fetched — OKX symbols
// (SOL-USDT) are never sent to Binance and vice-versa.
//
// As a side-effect it also syncs each exchange's server time (for exchanges that
// implement TimeSyncer) so subsequent signed REST requests carry the correct
// timestamp and avoid -1021 recvWindow errors from server clock drift.
func (r *Registry) PrewarmFilters(ctx context.Context, symbolsByExchange map[exchange.Exchange][]string) {
	// ── 1. Sync server time (sequential — one call per exchange) ──────────────
	for ex := range symbolsByExchange {
		if ts, ok := ex.(exchange.TimeSyncer); ok {
			if err := ts.SyncTime(ctx); err != nil {
				slog.Warn("prewarm: server time sync failed", "exchange", ex.Name(), "err", err)
			}
		}
	}

	// ── 2. Fetch symbol filters concurrently ──────────────────────────────────
	var wg sync.WaitGroup
	for ex, symbols := range symbolsByExchange {
		sip, ok := ex.(exchange.SymbolInfoProvider)
		if !ok {
			continue
		}
		r.market.filterViewFor(ex.Name())
		for _, sym := range symbols {
			wg.Add(1)
			go func(p exchange.SymbolInfoProvider, exName, s string) {
				defer safe.Recover()
				defer wg.Done()
				f, err := p.GetSymbolFilters(ctx, s)
				if err != nil {
					slog.Warn("prewarm: symbol filters fetch failed",
						"exchange", exName, "symbol", s, "err", err)
					return
				}
				r.market.setFilter(exName, s, f)
				slog.Info("prewarm: symbol filters ready",
					"exchange", exName, "symbol", s,
					"qty_step", f.QtyStep, "price_tick", f.PriceTick,
					"min_qty", f.MinQty, "min_notional", f.MinNotional)
			}(sip, ex.Name(), sym)
		}
	}
	wg.Wait()
}

// ── registryMetrics ───────────────────────────────────────────────────────────

// registryMetrics holds all runtime counters for the Registry.
type registryMetrics struct {
	routeNoHelm int64 // helm_id not found in registry    (atomic)
	routeNoHand int64 // hand_id not found in HelmRuntime (atomic)
}

func (m *registryMetrics) incNoHelm() { atomic.AddInt64(&m.routeNoHelm, 1) }
func (m *registryMetrics) incNoHand() { atomic.AddInt64(&m.routeNoHand, 1) }

func (m *registryMetrics) loadNoHelm() int64 { return atomic.LoadInt64(&m.routeNoHelm) }
func (m *registryMetrics) loadNoHand() int64 { return atomic.LoadInt64(&m.routeNoHand) }
