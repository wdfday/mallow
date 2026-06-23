package helm

import (
	"context"

	"go.uber.org/fx"
	"gorm.io/gorm"

	"mallow/helm/internal/infra/eventlog"
	"mallow/helm/internal/infra/orderlog"
	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/infra/purge"
	analyticsservice "mallow/helm/internal/module/analytics/service"
	brokerservice "mallow/helm/internal/module/broker/service"
	handservice "mallow/helm/internal/module/hand/service"
	"mallow/helm/internal/module/helm/domain"
	"mallow/helm/internal/module/helm/handler"
	"mallow/helm/internal/module/helm/repository"
	"mallow/helm/internal/module/helm/service"
	"mallow/helm/internal/runtime"
)

var Module = fx.Module("helm",
	fx.Provide(provideHelmRepo),
	fx.Provide(provideHelmService),
	fx.Provide(provideHelmHandler),
	fx.Provide(provideHelmNATSHandler),
	fx.Invoke(wireHelmDeps),
	fx.Invoke(hydrateRuntimes),
)

func provideHelmRepo(db *gorm.DB) domain.HelmRepo {
	return repository.New(db)
}

func provideHelmService(repo domain.HelmRepo, reg *runtime.Registry) *service.Service {
	return service.New(repo, reg)
}

func provideHelmHandler(
	svc *service.Service,
	handMgr *handservice.Service,
	reg *runtime.Registry,
	fillLog *perflog.FillLog,
	posLog poslog.Log,
	orderLog orderlog.Log,
	analytics *analyticsservice.Service,
	evLog eventlog.Log,
) *handler.Handler {
	return handler.New(svc, handMgr, reg, fillLog, posLog, orderLog, analytics, evLog)
}

func provideHelmNATSHandler(svc *service.Service, handMgr *handservice.Service, reg *runtime.Registry) *handler.NATSHandler {
	return handler.NewNATSHandler(svc, handMgr, reg)
}

// wireHelmDeps injects all cross-cutting ports into the helm service and registry.
// Must run before hydrateRuntimes (CredentialFetcher required by HydrateAll).
func wireHelmDeps(
	helmSvc *service.Service,
	handMgr *handservice.Service,
	brokerSvc brokerservice.BrokerConnectionService,
	repo domain.HelmRepo,
	db *gorm.DB,
	reg *runtime.Registry,
) {
	helmSvc.SetHandLifecycle(handMgr)
	helmSvc.SetCredentialFetcher(service.BrokerCredentialAdapter{BrokerSvc: brokerSvc})
	helmSvc.SetDataPurger(purge.New(db))
	reg.SetSyncStore(repo)
}

// hydrateRuntimes spawns HelmRuntime for every non-disabled helm on startup.
func hydrateRuntimes(helmSvc *service.Service) error {
	return helmSvc.HydrateAll(context.Background())
}
