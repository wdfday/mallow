package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/infra/poslog"
	helmdomain "mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/runtime/perf"
)

// ExchangeFactory creates per-account exchange adapters from an ExchangeConfig.
type ExchangeFactory interface {
	New(cfg helmdomain.ExchangeConfig) (exchange.Exchange, error)
}

// MarketStreamerFactory creates shared market data streaming clients per broker type.
// Returns nil if the broker does not support market streaming.
type MarketStreamerFactory interface {
	New(cfg helmdomain.ExchangeConfig) exchange.MarketStreamer
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
// MarketStreamers are shared per broker type.
type Registry struct {
	mu              sync.RWMutex
	helmRuntimes    map[uuid.UUID]*HelmRuntime
	marketStreamers map[string]exchange.MarketStreamer // broker_type → shared streamer

	// l2Books holds the latest L2 (order book) snapshot per broker type and symbol.
	// Keyed as l2Books[brokerType][symbol]. Shared across all helms of the same broker.
	// A single handler per streamer feeds this cache and fans-out to all watching hands.
	l2Books map[string]map[string]exchange.L2Snapshot
	l2Mu    sync.RWMutex

	exchFactory     ExchangeFactory
	streamerFactory MarketStreamerFactory

	// nc, js, and runCtx are set once via SetRuntime after startup.
	nc           *nats.Conn
	js           nats.JetStreamContext
	runCtx       context.Context
	syncStore    SyncStore
	posLog       poslog.Log        // nil when NATS unavailable
	portfolioLog perf.PortfolioLog // nil when NATS unavailable
}

// NewRegistry creates an empty Registry.
func NewRegistry(factory ExchangeFactory, streamerFactory MarketStreamerFactory) *Registry {
	return &Registry{
		helmRuntimes:    make(map[uuid.UUID]*HelmRuntime),
		marketStreamers: make(map[string]exchange.MarketStreamer),
		l2Books:         make(map[string]map[string]exchange.L2Snapshot),
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

// SetPortfolioLog injects the portfolio snapshot log (breaks init cycle — called after NATS connects).
// Also propagates to all already-spawned runtimes so runtimes created before startup
// (e.g. during SpawnAll) get the log injected retroactively.
func (r *Registry) SetPortfolioLog(log perf.PortfolioLog) {
	r.mu.Lock()
	r.portfolioLog = log
	for _, rt := range r.helmRuntimes {
		rt.PortfolioLog = log
	}
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
