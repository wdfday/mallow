package app

import (
	"context"
	"log/slog"

	"github.com/gin-gonic/gin"
	"github.com/nats-io/nats.go"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"mallow/helm/internal/config"
	"mallow/helm/internal/infra"
	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/module/hand/domain"
	handhandler "mallow/helm/internal/module/hand/handler"
	handrepo "mallow/helm/internal/module/hand/repository"
	"mallow/helm/internal/module/hand/service"
	orchdomain "mallow/helm/internal/module/helm/domain"
	orchhandler "mallow/helm/internal/module/helm/handler"
	helmrepo "mallow/helm/internal/module/helm/repository"
	orchservice "mallow/helm/internal/module/helm/service"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/tick"
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

	// Position event log (JetStream-backed; nil-safe when NATS unavailable)
	fx.Provide(newPosLog),

	// Repositories — Postgres required (POSTGRES_URL must be set)
	fx.Provide(newHelmRepo),
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
	fx.Provide(newHandHandler),
	fx.Provide(newServer),

	// Handlers — NATS (request/reply)
	fx.Provide(newOrchNATSHandler),
	fx.Provide(newHandNATSHandler),

	// Lifecycle hooks (order matters)
	fx.Invoke(wireHandLifecycle),
	fx.Invoke(wireSyncStore),
	fx.Invoke(wirePosLog),
	fx.Invoke(hydrateRuntimes), // helm runtimes must be ready before hands are hydrated
	fx.Invoke(hydrateHands),    // depends on hydrateRuntimes having run first
	fx.Invoke(subscribeSignals),
	fx.Invoke(startNATSAPI),
	fx.Invoke(runOrchestrator),
)

// asRuntimeFactory adapts *exchangeFactory to the runtime.ExchangeFactory interface.
func asRuntimeFactory(f *exchangeFactory) runtime.ExchangeFactory { return f }

// asStreamerFactory adapts *marketStreamerFactory to the runtime.MarketStreamerFactory interface.
func asStreamerFactory(f *marketStreamerFactory) runtime.MarketStreamerFactory { return f }

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

func newBarBuilder(cfg *config.Config, sc *engine.SignalClient) *tick.BarBuilder {
	return tick.NewBarBuilder(cfg.Runtime.BarInterval, func(bar *engine.BarMsg) {
		if err := sc.PublishBar(context.Background(), bar); err != nil {
			slog.Error("failed to publish bar", "symbol", bar.S, "err", err)
		}
	})
}

func newOrchHandler(svc *orchservice.Service, handMgr *service.Service, reg *runtime.Registry) *orchhandler.Handler {
	return orchhandler.New(svc, handMgr, reg)
}

func newHandHandler(handMgr *service.Service, helmSvc *orchservice.Service, sc *engine.SignalClient, reg *runtime.Registry) *handhandler.Handler {
	return handhandler.New(handMgr, helmSvc, sc, reg)
}

func newServer(orchH *orchhandler.Handler, handH *handhandler.Handler) *gin.Engine {
	return NewServer(orchH, handH)
}

func newOrchNATSHandler(svc *orchservice.Service, handMgr *service.Service, reg *runtime.Registry) *orchhandler.NATSHandler {
	return orchhandler.NewNATSHandler(svc, handMgr, reg)
}

func newHandNATSHandler(handMgr *service.Service, helmSvc *orchservice.Service) *handhandler.NATSHandler {
	return handhandler.NewNATSHandler(handMgr, helmSvc)
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

// wireHandLifecycle injects the hand lifecycle port into orchservice.Service (breaks init cycle).
func wireHandLifecycle(helmSvc *orchservice.Service, handMgr *service.Service) {
	helmSvc.SetHandLifecycle(handMgr)
}

// wireSyncStore injects the HelmRepo as the SyncStore for last-sync persistence.
func wireSyncStore(repo orchdomain.HelmRepo, reg *runtime.Registry) {
	reg.SetSyncStore(repo)
}

// hydrateRuntimes loads all active helm configs from DB and spawns their runtimes.
func hydrateRuntimes(repo orchdomain.HelmRepo, reg *runtime.Registry) error {
	cfgs, err := repo.All()
	if err != nil {
		return err
	}
	reg.SpawnAll(cfgs)
	slog.Info("runtimes hydrated", "count", len(cfgs))
	return nil
}

// hydrateHands loads all persisted hands from DB and wires them into the hand service.
// Must run AFTER hydrateRuntimes so that helm runtimes exist in the registry.
func hydrateHands(svc *service.Service) {
	svc.HydrateAll()
}
