package app

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	"github.com/shopspring/decimal"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"mallow/helm/internal/config"
	"mallow/helm/internal/infra"
	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/infra/eventlog"
	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/infra/tradelog"
	accountmodule "mallow/helm/internal/module/account"
	accountHandler "mallow/helm/internal/module/account/handler"
	accountrepo "mallow/helm/internal/module/account/repository"
	accountservice "mallow/helm/internal/module/account/service"
	analyticsservice "mallow/helm/internal/module/analytics/service"
	brokermodule "mallow/helm/internal/module/broker"
	brokerHandler "mallow/helm/internal/module/broker/handler"
	brokerepo "mallow/helm/internal/module/broker/repository"
	brokerservice "mallow/helm/internal/module/broker/service"
	"mallow/helm/internal/module/hand/domain"
	handhandler "mallow/helm/internal/module/hand/handler"
	handrepo "mallow/helm/internal/module/hand/repository"
	"mallow/helm/internal/module/hand/service"
	orchdomain "mallow/helm/internal/module/helm/domain"
	orchhandler "mallow/helm/internal/module/helm/handler"
	helmrepo "mallow/helm/internal/module/helm/repository"
	orchservice "mallow/helm/internal/module/helm/service"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/perf"
)

// Module declares all orchestrator components and lifecycle.
var Module = fx.Options(
	config.Module,
	infra.Module,
	accountmodule.Module,
	brokermodule.Module,

	// Exchange factories
	fx.Provide(newExchangeFactory),
	fx.Provide(asRuntimeFactory),
	fx.Provide(newMarketStreamerFactory),
	fx.Provide(asStreamerFactory),

	// Signal engine client
	fx.Provide(engine.NewSignalClient),

	// Adapt *runtime.Registry → runtime.SignalSink for SignalDispatcher
	fx.Provide(func(r *runtime.Registry) runtime.SignalSink { return r }),

	// Position event log (JetStream-backed; nil-safe when NATS unavailable)
	fx.Provide(newPosLog),

	// Event log — PostgreSQL-backed persistent activity store
	fx.Provide(newEventLog),
	fx.Provide(newEventLogPersister),

	// Snapshot persister — NATS → batch PG writer for equity_snapshots
	fx.Provide(newSnapshotPersister),

	// Trade persister — NATS HELM_TRADES → batch PG writer for trades
	fx.Provide(newTradePersister),
	// Fill persister — NATS TRADE_FILLS → batch PG writer for fills
	fx.Provide(newFillPersister),
	// tradelog.Log — read-only PG view of the trades table, consumed by handlers
	fx.Provide(newTradelogReader),
	// Snapshot reader — PG-backed read for equity_snapshots (curve + lag)
	fx.Provide(newSnapshotReader),
	// Analytics service — read-only KPI / curve / trade list
	fx.Provide(newStatsRunner),
	fx.Provide(newAnalyticsService),

	// Repositories — Postgres required (POSTGRES_URL must be set)
	fx.Provide(newHelmRepo),
	fx.Provide(newBotRepo),

	// Runtime registry
	fx.Provide(runtime.NewRegistry),
	fx.Provide(runtime.NewSignalDispatcher),

	// Services
	fx.Provide(newOrchService),
	fx.Provide(newBotService),

	// Handlers — Gin (HTTP)
	fx.Provide(newOrchHandler),
	fx.Provide(newHandHandler),
	fx.Provide(newAccountHandler),
	fx.Provide(newServer),

	// Handlers — NATS (request/reply)
	fx.Provide(newOrchNATSHandler),
	fx.Provide(newHandNATSHandler),

	// Lifecycle hooks (order matters)
	fx.Invoke(runMigrations),
	fx.Invoke(startEventLogPersister),
	fx.Invoke(startSnapshotPersister),
	fx.Invoke(startTradePersister),
	fx.Invoke(startFillPersister),
	fx.Invoke(wireHandLifecycle),
	fx.Invoke(wireSyncStore),
	fx.Invoke(wirePosLog),
	fx.Invoke(wireTradeLog),
	fx.Invoke(hydrateRuntimes), // helm runtimes must be ready before hands are hydrated
	fx.Invoke(hydrateHands),    // depends on hydrateRuntimes having run first
	fx.Invoke(subscribeHeraldReady),
	fx.Invoke(startHeartbeatLoop),
	fx.Invoke(startNATSAPI),
	fx.Invoke(runOrchestrator),
	fx.Invoke(subscribeSignals),
)

// asRuntimeFactory adapts *exchangeFactory to the runtime.ExchangeFactory interface.
func asRuntimeFactory(f *exchangeFactory) runtime.ExchangeFactory { return f }

// asStreamerFactory adapts *marketStreamerFactory to the runtime.MarketStreamerFactory interface.
func asStreamerFactory(f *marketStreamerFactory) runtime.MarketStreamerFactory { return f }

func runMigrations(db *gorm.DB) error {
	if err := accountrepo.Migrate(db); err != nil {
		return err
	}
	if err := brokerepo.Migrate(db); err != nil {
		return err
	}
	if err := helmrepo.Migrate(db); err != nil {
		return err
	}
	if err := handrepo.Migrate(db); err != nil {
		return err
	}
	if err := eventlog.Migrate(db); err != nil {
		return err
	}
	if err := tradelog.Migrate(db); err != nil {
		return err
	}
	if err := perflog.MigrateFills(db); err != nil {
		return err
	}
	return perflog.MigrateSnapshots(db)
}

func newHelmRepo(db *gorm.DB) orchdomain.HelmRepo {
	return helmrepo.New(db)
}

func newBotRepo(db *gorm.DB) domain.HandRepo {
	return handrepo.New(db)
}

func newOrchService(repo orchdomain.HelmRepo, reg *runtime.Registry) *orchservice.Service {
	return orchservice.New(repo, reg)
}

func newBotService(r domain.HandRepo, reg *runtime.Registry, sc *engine.SignalClient) *service.Service {
	return service.NewService(r, reg, sc)
}

func newOrchHandler(
	svc *orchservice.Service,
	handMgr *service.Service,
	reg *runtime.Registry,
	nc *nats.Conn,
	fillLog *perflog.FillLog,
	snapshotLog perf.SnapshotLog,
	posLog poslog.Log,
	analytics *analyticsservice.Service,
) *orchhandler.Handler {
	return orchhandler.New(svc, handMgr, reg, nc, fillLog, snapshotLog, posLog, analytics)
}

func newTradelogReader(db *sql.DB) tradelog.Log {
	return tradelog.New(db)
}

func newHandHandler(handMgr *service.Service, helmSvc *orchservice.Service, reg *runtime.Registry, evLog eventlog.Log, analytics *analyticsservice.Service) *handhandler.Handler {
	return handhandler.New(handMgr, helmSvc, reg, evLog, analytics)
}

func newEventLog(db *sql.DB) eventlog.Log {
	return eventlog.New(db)
}

func newEventLogPersister(js nats.JetStreamContext, db *sql.DB) *eventlog.Persister {
	return eventlog.NewPersister(js, db)
}

func newSnapshotPersister(js nats.JetStreamContext, db *sql.DB) *perflog.SnapshotPersister {
	return perflog.NewSnapshotPersister(js, db)
}

func startSnapshotPersister(lc fx.Lifecycle, p *perflog.SnapshotPersister) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go p.Run(ctx)
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			return nil
		},
	})
}

func startEventLogPersister(lc fx.Lifecycle, p *eventlog.Persister) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go p.Run(ctx)
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			return nil
		},
	})
}

func newAccountHandler(
	svc accountservice.Service,
	helmSvc *orchservice.Service,
	handMgr *service.Service,
	reg *runtime.Registry,
	nc *nats.Conn,
	fillLog *perflog.FillLog,
	snapshotLog perf.SnapshotLog,
	posLog poslog.Log,
	logger *slog.Logger,
) *accountHandler.Handler {
	return accountHandler.NewHandler(svc, helmSvc, handMgr, reg, nc, fillLog, snapshotLog, posLog, logger)
}

func newServer(orchH *orchhandler.Handler, handH *handhandler.Handler, accountH *accountHandler.Handler, brokerH *brokerHandler.BrokerConnectionHandler) *gin.Engine {
	return NewServer(orchH, handH, accountH, brokerH)
}

func newOrchNATSHandler(svc *orchservice.Service, handMgr *service.Service, reg *runtime.Registry) *orchhandler.NATSHandler {
	return orchhandler.NewNATSHandler(svc, handMgr, reg)
}

func newHandNATSHandler(handMgr *service.Service, helmSvc *orchservice.Service, reg *runtime.Registry) *handhandler.NATSHandler {
	return handhandler.NewNATSHandler(handMgr, helmSvc, reg)
}

// newPosLog creates the JetStream-backed position event log.
// Returns nil (not an error) when NATS is unavailable, so dev/test runs without NATS still work.
func newPosLog(js nats.JetStreamContext) poslog.Log {
	log, err := poslog.NewNatsLog(js)
	if err != nil {
		slog.Warn("poslog: JetStream init failed — position events will not be persisted", "err", err)
		return nil
	}
	return log
}

// wirePosLog injects the position event log into the registry so all new orchestrators receive it.
func wirePosLog(reg *runtime.Registry, log poslog.Log) {
	if log != nil {
		reg.SetPosLog(log)
	}
}

// wireTradeLog injects the JetStream perf.TradeLog into the registry so all
// runtimes (current and future) publish completed trades to HELM_TRADES.
// Trades are drained into Postgres asynchronously by TradePersister.
func wireTradeLog(reg *runtime.Registry, log perf.TradeLog) {
	reg.SetTradeLog(log)
}

func newTradePersister(js nats.JetStreamContext, db *sql.DB) *perflog.TradePersister {
	return perflog.NewTradePersister(js, db)
}

func newSnapshotReader(db *sql.DB) perflog.SnapshotReader {
	return perflog.NewSnapshotReader(db)
}

func newStatsRunner(db *sql.DB) analyticsservice.StatsRunner {
	return analyticsservice.NewPostgresStatsRunner(db)
}

func newAnalyticsService(
	trades tradelog.Log,
	snapshots perflog.SnapshotReader,
	stats analyticsservice.StatsRunner,
) *analyticsservice.Service {
	return analyticsservice.New(trades, snapshots, stats)
}

func startTradePersister(lc fx.Lifecycle, p *perflog.TradePersister) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go p.Run(ctx)
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			return nil
		},
	})
}

// wireHandLifecycle injects the hand lifecycle port into orchservice.Service (breaks init cycle).
func wireHandLifecycle(helmSvc *orchservice.Service, handMgr *service.Service) {
	helmSvc.SetHandLifecycle(handMgr)
}

// wireSyncStore injects the HelmRepo as the SyncStore for last-sync persistence.
func wireSyncStore(repo orchdomain.HelmRepo, reg *runtime.Registry) {
	reg.SetSyncStore(repo)
}

// hydrateRuntimes loads all active helm configs from DB and spawns their runtimes.
// Exchange credentials and metadata are fetched directly from the broker service (DB), never via NATS.
// Initial capital starts at zero and is updated on the first portfolio sync.
func hydrateRuntimes(repo orchdomain.HelmRepo, reg *runtime.Registry, brokerSvc brokerservice.BrokerConnectionService) error {
	cfgs, err := repo.All()
	if err != nil {
		return err
	}
	spawned := 0
	for _, cfg := range cfgs {
		if cfg.Status == orchdomain.HelmStatusHalted || cfg.Status == orchdomain.HelmStatusDisabled {
			continue
		}
		creds, err := brokerSvc.GetCredentialsByAccountID(context.Background(), cfg.AccountID.String())
		if err != nil {
			slog.Error("runtimes hydrate: fetch credentials failed", "helm_id", cfg.ID, "account_id", cfg.AccountID, "err", err)
			continue
		}
		exchCfg := orchdomain.ExchangeConfig{
			BrokerType:  cfg.BrokerType,
			AccountType: orchdomain.AccountType(creds.AccountType),
			AccountID:   creds.AccountRef,
			BaseURL:     creds.BaseURL,
			APIKey:      creds.APIKey,
			APISecret:   creds.APISecret,
			Passphrase:  creds.Passphrase,
			Paper:       creds.Paper,
		}
		if err := reg.Spawn(cfg, exchCfg, decimal.Zero); err != nil {
			slog.Error("runtime: spawn failed", "helm_id", cfg.ID, "err", err)
			continue
		}
		if cfg.Status == orchdomain.HelmStatusPaused {
			if rt, err := reg.Get(cfg.ID); err == nil {
				rt.Pause()
				slog.Info("runtime: spawned paused", "helm_id", cfg.ID)
			}
		}
		spawned++
	}
	slog.Info("runtimes hydrated", "count", spawned, "total", len(cfgs))
	return nil
}

// hydrateHands loads all persisted hands from DB and wires them into the hand service.
// Must run AFTER hydrateRuntimes so that helm runtimes exist in the registry.
func hydrateHands(svc *service.Service) {
	svc.HydrateAll()
}

func newFillPersister(js nats.JetStreamContext, db *sql.DB) *perflog.FillPersister {
	return perflog.NewFillPersister(js, db)
}

func startFillPersister(lc fx.Lifecycle, p *perflog.FillPersister) {
	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStart: func(_ context.Context) error {
			go p.Run(ctx)
			return nil
		},
		OnStop: func(_ context.Context) error {
			cancel()
			return nil
		},
	})
}
