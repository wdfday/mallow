package broker

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"mallow/helm/internal/infra/natsapi"
	accountRepo "mallow/helm/internal/module/account/repository"
	"mallow/helm/internal/module/broker/client/alpaca"
	"mallow/helm/internal/module/broker/client/binance"
	"mallow/helm/internal/module/broker/client/bybit"
	"mallow/helm/internal/module/broker/client/okx"
	"mallow/helm/internal/module/broker/domain"
	"mallow/helm/internal/module/broker/handler"
	repository2 "mallow/helm/internal/module/broker/repository"
	service2 "mallow/helm/internal/module/broker/service"
	helmDomain "mallow/helm/internal/module/helm/domain"
	helmDto "mallow/helm/internal/module/helm/dto"
	helmService "mallow/helm/internal/module/helm/service"
	internalService "mallow/helm/internal/service"
)

// Module provides broker connection management dependencies.
// Not yet wired into the router — call handler.RegisterRoutes when ready.
var Module = fx.Module("broker",
	fx.Provide(
		// Broker clients
		okx.NewOKXClient,
		binance.NewClient,
		alpaca.NewClient,
		bybit.NewClient,

		// Registry assembles all clients into a map
		provideBrokerRegistry,

		// Repository
		provideBrokerConnectionRepository,

		// Service
		provideBrokerConnectionService,

		// Handler
		provideBrokerConnectionHandler,
	),
)

func provideBrokerRegistry(
	okxClient *okx.OKXClient,
	binanceClient *binance.Client,
	alpacaClient *alpaca.Client,
	bybitClient *bybit.Client,
) service2.BrokerRegistry {
	return service2.BrokerRegistry{
		domain.BrokerTypeOKX:     okxClient,
		domain.BrokerTypeBinance: binanceClient,
		domain.BrokerTypeAlpaca:  alpacaClient,
		domain.BrokerTypeBybit:   bybitClient,
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
	return service2.NewBrokerConnectionService(repo, accRepo, encryptionService, clients, &helmCreatorAdapter{svc: helmSvc})
}

// helmCreatorAdapter adapts *helmService.Service to service2.HelmCreator.
type helmCreatorAdapter struct {
	svc *helmService.Service
}

func (a *helmCreatorAdapter) AutoCreateForAccount(ctx context.Context, req service2.HelmAutoCreateReq) error {
	_, err := a.svc.CreateForAccount(helmDto.CreateForAccountReq{
		UserID:    req.UserID,
		AccountID: req.AccountID,
		Name:      req.AccountName,
		Exchange: helmDomain.ExchangeConfig{
			BrokerType:  req.BrokerType,
			AccountType: helmDomain.AccountType(req.AccountType),
			Paper:       req.Paper,
		},
		Creds: natsapi.CredentialsFetchResp{
			APIKey:      req.APIKey,
			APISecret:   req.APISecret,
			Passphrase:  req.Passphrase,
			BrokerType:  req.BrokerType,
			AccountType: req.AccountType,
			Paper:       req.Paper,
		},
	})
	return err
}

func (a *helmCreatorAdapter) AutoDeleteForAccount(_ context.Context, accountID uuid.UUID) error {
	return a.svc.DeleteForAccount(accountID)
}

func provideBrokerConnectionHandler(
	svc service2.BrokerConnectionService,
	logger *slog.Logger,
) *handler.BrokerConnectionHandler {
	return handler.NewBrokerConnectionHandler(svc, logger)
}
