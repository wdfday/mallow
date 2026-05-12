package broker

import (
	"log/slog"

	"github.com/nats-io/nats.go"
	"go.uber.org/fx"
	"gorm.io/gorm"

	accountRepo "mallow/investment/internal/module/account/repository"
	"mallow/investment/internal/module/broker/client/alpaca"
	"mallow/investment/internal/module/broker/client/binance"
	"mallow/investment/internal/module/broker/client/bybit"
	"mallow/investment/internal/module/broker/client/okx"
	"mallow/investment/internal/module/broker/domain"
	"mallow/investment/internal/module/broker/handler"
	repository2 "mallow/investment/internal/module/broker/repository"
	service2 "mallow/investment/internal/module/broker/service"
	internalService "mallow/investment/internal/service"
)

// Module provides broker connection management dependencies.
// Syncing is handled by the orchestrator — investment service only validates
// credentials and stores the connection on creation.
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

	// Wire NATS credential fetch handler so helm can request decrypted credentials at runtime.
	fx.Invoke(func(svc service2.BrokerConnectionService, nc *nats.Conn) error {
		return svc.SubscribeCredentials(nc)
	}),
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
	nc *nats.Conn,
) service2.BrokerConnectionService {
	return service2.NewBrokerConnectionService(repo, accRepo, encryptionService, clients, nc)
}

func provideBrokerConnectionHandler(
	svc service2.BrokerConnectionService,
	logger *slog.Logger,
) *handler.BrokerConnectionHandler {
	return handler.NewBrokerConnectionHandler(svc, logger)
}
