package hand

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"
	"gorm.io/gorm"

	"mallow/helm/internal/fleet"
	"mallow/helm/internal/infra/journal/eventlog"
	analyticsservice "mallow/helm/internal/module/analytics/service"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/module/hand/handler"
	"mallow/helm/internal/module/hand/repository"
	"mallow/helm/internal/module/hand/service"
	helmservice "mallow/helm/internal/module/helm/service"
)

var Module = fx.Module("hand",
	fx.Provide(provideHandRepo),
	fx.Provide(provideHandService),
	fx.Provide(provideHandHandler),
	fx.Provide(provideHandNATSHandler),
	fx.Invoke(hydrateHands),
)

func provideHandRepo(db *gorm.DB) domain.HandRepo {
	return repository.New(db)
}

func provideHandService(repo domain.HandRepo, reg *fleet.Registry) *service.Service {
	return service.NewService(repo, reg)
}

func provideHandHandler(
	handMgr *service.Service,
	helmSvc *helmservice.Service,
	reg *fleet.Registry,
	evLog eventlog.Log,
	analytics *analyticsservice.Service,
) *handler.Handler {
	return handler.New(handMgr, helmSvc, reg, evLog, analytics)
}

func provideHandNATSHandler(handMgr *service.Service, helmSvc *helmservice.Service, reg *fleet.Registry) *handler.NATSHandler {
	return handler.NewNATSHandler(handMgr, helmSvc, reg)
}

// hydrateHands loads all persisted hands and prewarms exchange symbol-filter caches.
// Must run after hydrateRuntimes (helm runtimes must exist in registry first).
func hydrateHands(svc *service.Service, reg *fleet.Registry) {
	svc.HydrateAll()
	symsByEx := svc.SymbolsByExchange()
	total := 0
	for _, ss := range symsByEx {
		total += len(ss)
	}
	slog.Info("hands hydrated, prewarming symbol filters", "pairs", total)
	if total > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		reg.PrewarmFilters(ctx, symsByEx)
		slog.Info("symbol filters prewarm complete", "pairs", total)
	}
}
