package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"orchestrator/internal/infra/exchange"
	"orchestrator/internal/infra/natsapi"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	"orchestrator/internal/runtime/core/portfolio"
	"orchestrator/internal/runtime/core/risk"
	"orchestrator/internal/runtime/orderbook"
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
// Implemented by domain.OrchestratorRepo.
type SyncStore interface {
	UpdateLastSyncedAt(id uuid.UUID, t time.Time) error
}

// Registry manages all live OrchestratorRuntime instances.
// One runtime per active orchestrator config.
// OrderBooks and MarketStreamers are shared per broker type.
type Registry struct {
	mu              sync.RWMutex
	runtimes        map[uuid.UUID]*OrchestratorRuntime
	orderBooks      map[string]orderbook.OrderBook     // broker_type → shared OrderBook
	marketStreamers map[string]exchange.MarketStreamer // broker_type → shared streamer
	signalCh        chan RoutedSignal

	exchFactory     ExchangeFactory
	streamerFactory MarketStreamerFactory
	signalLoopOnce  sync.Once

	// nc, js, and runCtx are set once via SetRuntime after startup.
	nc        *nats.Conn
	js        nats.JetStreamContext
	runCtx    context.Context
	syncStore SyncStore
}

// NewRegistry creates an empty Registry.
func NewRegistry(factory ExchangeFactory, streamerFactory MarketStreamerFactory) *Registry {
	return &Registry{
		runtimes:        make(map[uuid.UUID]*OrchestratorRuntime),
		orderBooks:      make(map[string]orderbook.OrderBook),
		marketStreamers: make(map[string]exchange.MarketStreamer),
		signalCh:        make(chan RoutedSignal, defaultRegistrySignalBuffer),
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

	r.startSignalLoop(ctx)
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
			slog.Warn("registry: sync failed", "orchestrator_id", id, "err", err)
			return
		}
		r.persistSyncTime(rt)
	}()
}

// OrderBook returns the shared OrderBook for a given broker type.
func (r *Registry) OrderBook(brokerType string) orderbook.OrderBook {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.orderBooks[brokerType]
}

// Spawn creates and registers an OrchestratorRuntime for the given config.
func (r *Registry) Spawn(cfg *orchdomain.OrchestratorConfig) error {
	ex, err := r.exchFactory.New(cfg.Exchange)
	if err != nil {
		return fmt.Errorf("registry: create exchange for %q: %w", cfg.ID, err)
	}

	pf := portfolio.New(cfg.Capital)
	riskCfg := risk.Config{
		MaxPositions:      cfg.Risk.MaxPositions,
		MaxPositionPct:    cfg.Risk.MaxPositionPct,
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

	rt := NewOrchestratorRuntime(cfg.ID, cfg.AccountID, cfg.UserID, cfg.Exchange.BrokerType, pf, riskMgr, ob, ex, cfg.LastSyncedAt)

	// Register this runtime's price updater with the shared market streamer.
	r.mu.RLock()
	ms := r.marketStreamers[cfg.Exchange.BrokerType]
	r.mu.RUnlock()
	if ms != nil {
		ms.AddPriceHandler(rt.UpdatePrice)
	}

	r.mu.Lock()
	r.runtimes[cfg.ID] = rt
	ctx := r.runCtx
	nc := r.nc
	r.mu.Unlock()

	slog.Info("runtime: spawned", "orchestrator_id", cfg.ID, "account_id", cfg.AccountID, "broker", cfg.Exchange.BrokerType)

	// If the app is already running (SetRuntime was called), start fill streaming immediately
	// so hot-plugged orchestrators (from accountLinked events) get WS fills right away.
	if ctx != nil && nc != nil {
		r.startFillStream(ctx, nc, rt)
	}
	return nil
}

// Teardown stops and removes the OrchestratorRuntime for the given orchestrator.
// Returns bot IDs that were registered so the caller can stop them.
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
		slog.Info("runtime: torn down", "orchestrator_id", id, "bots_orphaned", len(botIDs))
	}
	return botIDs
}

// Pause pauses a runtime: signals rejected, returns bot IDs that were running.
func (r *Registry) Pause(id uuid.UUID) ([]string, error) {
	r.mu.RLock()
	rt, ok := r.runtimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for orchestrator %q", id)
	}

	wasRunning := rt.Pause()
	slog.Info("runtime: paused", "orchestrator_id", id, "bots_stopped", len(wasRunning))
	return wasRunning, nil
}

// Resume resumes a paused runtime: returns bot IDs that should be restarted.
func (r *Registry) Resume(id uuid.UUID) ([]string, error) {
	r.mu.RLock()
	rt, ok := r.runtimes[id]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for orchestrator %q", id)
	}

	toRestart := rt.Resume()
	slog.Info("runtime: resumed", "orchestrator_id", id, "bots_restarting", len(toRestart))
	return toRestart, nil
}

// Get returns the OrchestratorRuntime for the given orchestrator ID.
func (r *Registry) Get(id uuid.UUID) (*OrchestratorRuntime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.runtimes[id]
	if !ok {
		return nil, fmt.Errorf("registry: no runtime for orchestrator %q", id)
	}
	return rt, nil
}

// All returns all active runtimes.
func (r *Registry) All() []*OrchestratorRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*OrchestratorRuntime, 0, len(r.runtimes))
	for _, rt := range r.runtimes {
		out = append(out, rt)
	}
	return out
}

// DispatchBotSignal routes a signal to a specific bot by scanning orchestrator runtimes.
// Returns true when the target bot is found and the signal is delivered.
func (r *Registry) DispatchBotSignal(botID string, sig Signal) bool {
	for _, rt := range r.All() {
		if rt.DispatchBotSignal(botID, sig) {
			return true
		}
	}
	return false
}

// persistSyncTime writes the runtime's lastSyncAt to the store (best-effort, logs on error).
func (r *Registry) persistSyncTime(rt *OrchestratorRuntime) {
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
		rts := make([]*OrchestratorRuntime, 0, len(r.runtimes))
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

// StartFillStreaming starts account fill listeners for all runtimes whose exchange
// implements AccountStreamer. Called once after SpawnAll, from the app lifecycle.
func (r *Registry) StartFillStreaming(ctx context.Context, nc *nats.Conn) {
	r.mu.RLock()
	rts := make([]*OrchestratorRuntime, 0, len(r.runtimes))
	for _, rt := range r.runtimes {
		rts = append(rts, rt)
	}
	r.mu.RUnlock()

	for _, rt := range rts {
		r.startFillStream(ctx, nc, rt)
	}
}

func (r *Registry) startFillStream(ctx context.Context, nc *nats.Conn, rt *OrchestratorRuntime) {
	streamer, ok := rt.Exchange.(exchange.AccountStreamer)
	if !ok {
		return
	}
	// WS callback only enqueues — never blocks on NATS.
	if err := streamer.StreamFills(ctx, rt.EnqueueFill); err != nil {
		slog.Error("fill stream start failed", "orchestrator_id", rt.OrchestratorID, "err", err)
		return
	}
	// Dedicated goroutine drains fillCh and publishes to NATS.
	go r.runFillProcessor(ctx, nc, rt)
	slog.Info("fill streaming started", "orchestrator_id", rt.OrchestratorID, "exchange", rt.Exchange.Name())
}

// runFillProcessor drains rt.fillCh and applies each fill:
// updates the in-memory portfolio, publishes FillNotification and InvestmentTransaction to NATS.
func (r *Registry) runFillProcessor(ctx context.Context, nc *nats.Conn, rt *OrchestratorRuntime) {
	for {
		select {
		case ev := <-rt.fillCh:
			r.mu.RLock()
			js := r.js
			r.mu.RUnlock()
			r.applyFill(nc, js, rt, ev)
		case <-ctx.Done():
			return
		}
	}
}

func (r *Registry) applyFill(nc *nats.Conn, js nats.JetStreamContext, rt *OrchestratorRuntime, ev exchange.FillEvent) {
	accountID := rt.OrchestratorID.String()

	// Resolve botID from pending orders before the fill removes the record.
	botID := ""
	for _, p := range rt.OrderBook.PendingOrders(accountID) {
		if p.OrderID == ev.OrderID {
			botID = p.BotID
			break
		}
	}

	rt.ReportFill(orchdomain.FillReport{
		BotID:     botID,
		AccountID: accountID,
		OrderID:   ev.OrderID,
		Symbol:    ev.Symbol,
		Side:      string(ev.Side),
		Qty:       ev.FilledQty,
		Price:     ev.FilledAvg,
		Timestamp: ev.Timestamp,
	})

	if nc == nil {
		return
	}
	subj := fmt.Sprintf(natsapi.SubjTradeFilled, rt.OrchestratorID)
	data, _ := json.Marshal(natsapi.FillNotification{
		OrchestratorID: accountID,
		BotID:          botID,
		OrderID:        ev.OrderID,
		Symbol:         ev.Symbol,
		Side:           string(ev.Side),
		FilledQty:      ev.FilledQty,
		FilledAvg:      ev.FilledAvg,
		Timestamp:      ev.Timestamp,
	})
	if err := nc.Publish(subj, data); err != nil {
		slog.Warn("fill: nats publish failed", "subject", subj, "err", err)
	}

	// Publish to investment JetStream for event sourcing. Dedup via Nats-Msg-Id.
	if js != nil {
		txn := natsapi.TransactionMsg{
			OrderID:  ev.OrderID,
			Symbol:   ev.Symbol,
			Side:     string(ev.Side),
			Qty:      ev.FilledQty,
			AvgPrice: ev.FilledAvg,
			FilledAt: ev.Timestamp,
		}
		natsapi.PublishInvestmentTransaction(js, rt.OrchestratorID.String(), rt.AccountID.String(), rt.UserID.String(), botID, rt.BrokerType, txn)
	}
}
