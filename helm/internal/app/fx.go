package app

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"mallow/helm/internal/config"
	"mallow/helm/internal/fleet"
	"mallow/helm/internal/fleet/dispatcher"
	"mallow/helm/internal/fleet/perf"
	"mallow/helm/internal/infra"
	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	"mallow/helm/internal/infra/herald"
	"mallow/helm/internal/infra/journal/eventlog"
	"mallow/helm/internal/infra/journal/filllog"
	"mallow/helm/internal/infra/journal/orderlog"
	"mallow/helm/internal/infra/journal/poslog"
	"mallow/helm/internal/infra/journal/signallog"
	"mallow/helm/internal/infra/journal/tradelog"
	accountmodule "mallow/helm/internal/module/account"
	accountHandler "mallow/helm/internal/module/account/handler"
	accountrepo "mallow/helm/internal/module/account/repository"
	accountservice "mallow/helm/internal/module/account/service"
	analyticsservice "mallow/helm/internal/module/analytics/service"
	brokermodule "mallow/helm/internal/module/broker"
	brokerHandler "mallow/helm/internal/module/broker/handler"
	brokerepo "mallow/helm/internal/module/broker/repository"
	handmodule "mallow/helm/internal/module/hand"
	handhandler "mallow/helm/internal/module/hand/handler"
	handrepo "mallow/helm/internal/module/hand/repository"
	helmmodule "mallow/helm/internal/module/helm"
	orchhandler "mallow/helm/internal/module/helm/handler"
	helmrepo "mallow/helm/internal/module/helm/repository"
)

// Module declares all orchestrator components and lifecycle.
var Module = fx.Options(
	config.Module,
	infra.Module,
	accountmodule.Module,
	brokermodule.Module,

	// Exchange factory — covers both execution adapters (New) and market data streaming (NewStreamer).
	fx.Provide(newExchangeFactory),
	fx.Provide(asRuntimeFactory),

	// Shared act.Client singletons for the broker module (credential validation / portfolio sync).
	// Credentials are passed per-call; these clients hold only an HTTP connection pool.
	fx.Provide(func() *okxact.Client { return okxact.New(okxact.Config{}) }),
	fx.Provide(func() *binanceact.Client { return binanceact.New(false) }),
	fx.Provide(func() *bybitact.Client { return bybitact.New(bybitact.Config{}) }),
	fx.Provide(func() *alpacaact.Client { return alpacaact.New(alpacaact.Config{}) }),

	// Signal engine client (signal subscription + bar publishing)
	fx.Provide(herald.NewSignalClient),
	// Herald registry client (register/deregister/validate/heartbeat)
	fx.Provide(func(nc *nats.Conn) *herald.Client { return herald.New(nc) }),

	// Adapt *fleet.Registry → dispatcher.SignalSink for SignalDispatcher
	fx.Provide(func(r *fleet.Registry) dispatcher.SignalSink { return r }),

	// Position event log (JetStream-backed; nil-safe when NATS unavailable)
	fx.Provide(newPosLog),

	// Event log — PostgreSQL-backed persistent activity store
	fx.Provide(newEventLog),
	fx.Provide(newEventLogPersister),
	fx.Provide(newOrdersPersister),
	fx.Provide(newTradePersister),
	fx.Provide(newFillPersister),
	fx.Provide(newSignalPersister),
	fx.Provide(newSignalLogReader),
	fx.Provide(newTradelogReader),
	fx.Provide(newOrderlogReader),
	fx.Provide(newStatsRunner),
	fx.Provide(newAnalyticsService),

	// Runtime registry
	fx.Provide(fleet.NewRegistry),
	fx.Provide(dispatcher.NewSignalDispatcher),

	// HTTP server (receives handlers from helm/hand modules via FX)
	fx.Provide(newAccountHandler),
	fx.Provide(newServer),

	// Lifecycle hooks — persisters and registry chains before hydration
	fx.Invoke(runMigrations),
	fx.Invoke(startEventLogPersister),
	fx.Invoke(startOrdersPersister),
	fx.Invoke(startTradePersister),
	fx.Invoke(startFillPersister),
	fx.Invoke(startSignalPersister),
	fx.Invoke(wirePosLog),
	fx.Invoke(wireTradeLog),
	fx.Invoke(wirePnLSummer),
	fx.Invoke(wireEventCounter),

	// Helm module: wires service deps + hydrates runtimes
	helmmodule.Module,

	// Hand module: hydrates hands (must be after helm runtimes are up)
	handmodule.Module,

	// fx.Invoke(syncBrokerAccounts), // one-shot migration: uncomment → restart → comment back
	// fx.Invoke(backfillTerminalMetrics), // one-shot: recompute FinalMetrics for killed/released hands → run once → comment back
	fx.Invoke(subscribeHeraldReady),
	fx.Invoke(startHeartbeatLoop),
	fx.Invoke(startNATSAPI),
	fx.Invoke(runOrchestrator),
	fx.Invoke(subscribeSignals),
)

// asRuntimeFactory adapts *exchangeFactory to the fleet.ExchangeFactory interface.
func asRuntimeFactory(f *exchangeFactory) fleet.ExchangeFactory { return f }

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
	if err := orderlog.Migrate(db); err != nil {
		return err
	}
	if err := filllog.MigrateFills(db); err != nil {
		return err
	}
	if err := tradelog.Migrate(db); err != nil {
		return err
	}
	if err := signallog.Migrate(db); err != nil {
		return err
	}
	return nil
}

func newOrderlogReader(db *sql.DB) orderlog.Log {
	return orderlog.NewLog(db)
}

func newTradelogReader(db *sql.DB) tradelog.Log {
	return tradelog.New(db)
}

func newEventLog(db *sql.DB) eventlog.Log {
	return eventlog.New(db)
}

func newEventLogPersister(js nats.JetStreamContext, db *sql.DB) *eventlog.Persister {
	return eventlog.NewPersister(js, db)
}

func newOrdersPersister(js nats.JetStreamContext, db *sql.DB) *orderlog.Persister {
	return orderlog.NewPersister(js, db)
}

func startOrdersPersister(lc fx.Lifecycle, p *orderlog.Persister) {
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

func newAccountHandler(svc accountservice.Service) *accountHandler.Handler {
	return accountHandler.NewHandler(svc)
}

func newServer(orchH *orchhandler.Handler, handH *handhandler.Handler, accountH *accountHandler.Handler, brokerH *brokerHandler.BrokerConnectionHandler) *gin.Engine {
	return NewServer(orchH, handH, accountH, brokerH)
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
func wirePosLog(reg *fleet.Registry, log poslog.Log) {
	if log != nil {
		reg.SetPosLog(log)
	}
}

// wireTradeLog injects the JetStream perf.TradeLog into the registry so all
// runtimes (current and future) publish completed trades to HELM_TRADES.
// Trades are drained into Postgres asynchronously by TradePersister.
func wireTradeLog(reg *fleet.Registry, log perf.TradeLog) {
	reg.SetTradeLog(log)
}

// wirePnLSummer injects the postgres tradelog reader as the PnLSummer so
// RestorePnL uses a single SQL aggregate query instead of draining JetStream.
func wirePnLSummer(reg *fleet.Registry, log tradelog.Log) {
	reg.SetPnLSummer(log)
}

// wireEventCounter injects the postgres event log as the EventCounter so
// RestoreCounters rebuilds activity counters from helm_events on startup.
func wireEventCounter(reg *fleet.Registry, log eventlog.Log) {
	reg.SetEventCounter(log)
}

func newTradePersister(js nats.JetStreamContext, db *sql.DB) *tradelog.TradePersister {
	return tradelog.NewTradePersister(js, db)
}

func newStatsRunner(db *sql.DB) analyticsservice.StatsRunner {
	return analyticsservice.NewPostgresStatsRunner(db)
}

func newAnalyticsService(
	trades tradelog.Log,
	stats analyticsservice.StatsRunner,
) *analyticsservice.Service {
	return analyticsservice.New(trades, stats)
}

func startTradePersister(lc fx.Lifecycle, p *tradelog.TradePersister) {
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

func newFillPersister(js nats.JetStreamContext, db *sql.DB) *filllog.FillPersister {
	return filllog.NewFillPersister(js, db)
}

func startFillPersister(lc fx.Lifecycle, p *filllog.FillPersister) {
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

func newSignalPersister(js nats.JetStreamContext, db *sql.DB) *signallog.SignalPersister {
	return signallog.NewSignalPersister(js, db)
}

func newSignalLogReader(db *sql.DB) *signallog.Log {
	return signallog.New(db)
}

func startSignalPersister(lc fx.Lifecycle, p *signallog.SignalPersister) {
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
