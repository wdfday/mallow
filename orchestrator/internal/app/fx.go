package app

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/gin-gonic/gin"
	"go.uber.org/fx"

	"orchestrator/internal/config"
	"orchestrator/internal/infra"
	"orchestrator/internal/infra/engine"
	pgstore "orchestrator/internal/infra/postgres"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	orchhandler "orchestrator/internal/module/orchesrator/handler"
	orchrepo "orchestrator/internal/module/orchesrator/repo"
	orchservice "orchestrator/internal/module/orchesrator/service"
	"orchestrator/internal/module/worker/domain"
	bothandler "orchestrator/internal/module/worker/handler"
	workerrepo "orchestrator/internal/module/worker/repo"
	"orchestrator/internal/module/worker/service"
	"orchestrator/internal/runtime"
	"orchestrator/internal/runtime/core/tick"
)

// Module declares all orchestrator components and lifecycle.
var Module = fx.Options(
	config.Module,
	infra.Module,

	// Exchange factories
	fx.Provide(newExchangeFactory),
	fx.Provide(asRuntimeFactory),
	fx.Provide(newMarketStreamerFactory),
	fx.Provide(asStreamerFactory),

	// Signal engine client
	fx.Provide(engine.NewSignalClient),

	// Repositories (Postgres when DSN set, memory fallback)
	fx.Provide(newOrchestratorRepo),
	fx.Provide(newBotRepo),

	// Runtime registry
	fx.Provide(runtime.NewRegistry),
	fx.Provide(runtime.NewSignalDispatcher),

	// Services
	fx.Provide(newOrchService),
	fx.Provide(newBotService),

	// Bar builder + tick router
	fx.Provide(newBarBuilder),
	fx.Provide(tick.NewRouter),

	// Handlers — Gin (HTTP)
	fx.Provide(newOrchHandler),
	fx.Provide(newBotHandler),
	fx.Provide(newServer),

	// Handlers — NATS (request/reply)
	fx.Provide(newOrchNATSHandler),
	fx.Provide(newBotNATSHandler),

	// Lifecycle hooks (order matters)
	fx.Invoke(wireBotLifecycle),
	fx.Invoke(wireSyncStore),
	fx.Invoke(hydrateRuntimes),
	fx.Invoke(subscribeSignals),
	fx.Invoke(startNATSAPI),
	fx.Invoke(runOrchestrator),
)

// asRuntimeFactory adapts *exchangeFactory to the runtime.ExchangeFactory interface.
func asRuntimeFactory(f *exchangeFactory) runtime.ExchangeFactory { return f }

// asStreamerFactory adapts *marketStreamerFactory to the runtime.MarketStreamerFactory interface.
func asStreamerFactory(f *marketStreamerFactory) runtime.MarketStreamerFactory { return f }

func newOrchestratorRepo(db *sql.DB) orchdomain.OrchestratorRepo {
	if db == nil {
		slog.Info("orchestrator repo: using memory (no postgres)")
		return orchrepo.NewMemoryOrchestratorStore()
	}
	return pgstore.NewOrchestratorStore(db)
}

func newBotRepo(db *sql.DB) domain.BotRepo {
	if db == nil {
		slog.Info("bot repo: using memory (no postgres)")
		return workerrepo.NewMemoryStore()
	}
	return pgstore.NewBotStore(db)
}

func newOrchService(repo orchdomain.OrchestratorRepo, reg *runtime.Registry) *orchservice.Service {
	return orchservice.New(repo, reg)
}

func newBotService(r domain.BotRepo, reg *runtime.Registry, sc *engine.SignalClient) *service.Service {
	return service.NewService(r, reg, sc)
}

func newBarBuilder(cfg *config.Config, sc *engine.SignalClient) *tick.BarBuilder {
	return tick.NewBarBuilder(cfg.Runtime.BarInterval, func(bar *engine.BarMsg) {
		if err := sc.PublishBar(context.Background(), bar); err != nil {
			slog.Error("failed to publish bar", "symbol", bar.S, "err", err)
		}
	})
}

func newOrchHandler(svc *orchservice.Service, botMgr *service.Service, reg *runtime.Registry) *orchhandler.Handler {
	return orchhandler.New(svc, botMgr, reg)
}

func newBotHandler(botMgr *service.Service, orchSvc *orchservice.Service, sc *engine.SignalClient, reg *runtime.Registry) *bothandler.Handler {
	return bothandler.New(botMgr, orchSvc, sc, reg)
}

func newServer(orchH *orchhandler.Handler, botH *bothandler.Handler) *gin.Engine {
	return NewServer(orchH, botH)
}

func newOrchNATSHandler(svc *orchservice.Service, botMgr *service.Service, reg *runtime.Registry) *orchhandler.NATSHandler {
	return orchhandler.NewNATSHandler(svc, botMgr, reg)
}

func newBotNATSHandler(botMgr *service.Service, orchSvc *orchservice.Service) *bothandler.NATSHandler {
	return bothandler.NewNATSHandler(botMgr, orchSvc)
}

// wireBotLifecycle injects the bot lifecycle port into orchservice.Service (breaks init cycle).
func wireBotLifecycle(orchSvc *orchservice.Service, botMgr *service.Service) {
	orchSvc.SetBotLifecycle(botMgr)
}

// wireSyncStore injects the OrchestratorRepo as the SyncStore for last-sync persistence.
func wireSyncStore(repo orchdomain.OrchestratorRepo, reg *runtime.Registry) {
	reg.SetSyncStore(repo)
}

// hydrateRuntimes loads all active orchestrator configs from DB and spawns their runtimes.
func hydrateRuntimes(repo orchdomain.OrchestratorRepo, reg *runtime.Registry) error {
	cfgs, err := repo.All()
	if err != nil {
		return err
	}
	reg.SpawnAll(cfgs)
	slog.Info("runtimes hydrated", "count", len(cfgs))
	return nil
}
