package broker

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.uber.org/fx"
	"gorm.io/gorm"

	alpacaact "mallow/helm/internal/infra/exchange/alpaca/act"
	binanceact "mallow/helm/internal/infra/exchange/binance/act"
	bybitact "mallow/helm/internal/infra/exchange/bybit/act"
	okxact "mallow/helm/internal/infra/exchange/okx/act"
	accountRepo "mallow/helm/internal/module/account/repository"
	"mallow/helm/internal/module/broker/domain"
	"mallow/helm/internal/module/broker/handler"
	repository2 "mallow/helm/internal/module/broker/repository"
	service2 "mallow/helm/internal/module/broker/service"
	helmService "mallow/helm/internal/module/helm/service"
	"mallow/helm/internal/runtime"
	internalService "mallow/helm/internal/service"
)

// Module provides broker connection management dependencies.
// Not yet wired into the router — call handler.RegisterRoutes when ready.
var Module = fx.Module("broker",
	fx.Provide(
		// Broker clients — wraps the shared act.Client singletons already provided by app/fx.go
		provideBrokerRegistry,

		// Repository
		provideBrokerConnectionRepository,

		// Service
		provideBrokerConnectionService,

		// Handler
		provideBrokerConnectionHandler,
	),
	fx.Invoke(wireCredentialErrorHook),
)

func provideBrokerRegistry(
	okxClient *okxact.Client,
	binanceClient *binanceact.Client,
	alpacaClient *alpacaact.Client,
	bybitClient *bybitact.Client,
) service2.BrokerRegistry {
	return service2.BrokerRegistry{
		domain.BrokerTypeOKX:     okxact.NewBroker(okxClient),
		domain.BrokerTypeBinance: binanceact.NewBroker(binanceClient),
		domain.BrokerTypeAlpaca:  alpacaact.NewBroker(alpacaClient),
		domain.BrokerTypeBybit:   bybitact.NewBroker(bybitClient),
	}
}

func provideBrokerConnectionRepository(db *gorm.DB) repository2.BrokerConnectionRepository {
	return repository2.NewGormBrokerConnectionRepository(db)
}

func provideBrokerConnectionService(
	repo repository2.BrokerConnectionRepository,
	accRepo accountRepo.Repository,
	encryptionService *internalService.EncryptionService,
	clients service2.BrokerRegistry,
	helmSvc *helmService.Service,
) service2.BrokerConnectionService {
	return service2.NewBrokerConnectionService(repo, accRepo, encryptionService, clients, &helmService.BrokerAdapter{Svc: helmSvc})
}

func provideBrokerConnectionHandler(
	svc service2.BrokerConnectionService,
	logger *slog.Logger,
) *handler.BrokerConnectionHandler {
	return handler.NewBrokerConnectionHandler(svc, logger)
}

// wireCredentialErrorHook sets the registry callback that fires when a running
// HelmRuntime receives an exchange auth error. The hook marks the broker connection
// as error in the DB (via brokerSvc.MarkCredentialError) so the UI can surface it.
// The helm is already self-paused by TriggerAuthError before this callback runs.
func wireCredentialErrorHook(reg *runtime.Registry, brokerSvc service2.BrokerConnectionService) {
	reg.SetCredentialErrorHook(func(accountID uuid.UUID, reason string) {
		if err := brokerSvc.MarkCredentialError(context.Background(), accountID, reason); err != nil {
			slog.Error("credential error hook: failed to mark broker connection error",
				"account_id", accountID, "err", err)
		}
	})
}
