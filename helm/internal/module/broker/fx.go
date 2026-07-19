package broker

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.uber.org/fx"
	"gorm.io/gorm"

	"mallow/helm/internal/fleet"
	"mallow/helm/internal/fleet/actor"
	"mallow/helm/internal/infra/exchange"
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
	fx.Invoke(wireAccountEventHandler),
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

// accountEventHandler implements actor.AccountEventHandler, routing each
// account/connection-level condition to the broker service. The runtime is
// already self-paused (TriggerAccountError) before any of these run.
type accountEventHandler struct {
	brokerSvc service2.BrokerConnectionService
}

// OnAccountError marks the broker connection as error in the DB for the auth
// case specifically — an auth rejection means this connection's credentials
// are no longer usable, same DB-level consequence MarkCredentialError already
// existed for. Other escalating classes (sustained network/exchange-server
// errors) are logged only for now — they're transient infra conditions, not
// necessarily "this connection's credentials are bad".
func (h *accountEventHandler) OnAccountError(accountID uuid.UUID, class exchange.ErrClass, reason string) {
	if class != exchange.ErrClassAuth {
		slog.Warn("account error (non-auth, logged only)",
			"account_id", accountID, "class", exchange.ErrClassName[class], "reason", reason)
		return
	}
	if err := h.brokerSvc.MarkCredentialError(context.Background(), accountID, reason); err != nil {
		slog.Error("account event: failed to mark broker connection error",
			"account_id", accountID, "err", err)
	}
}

// OnMarginCall logs the margin-ratio/liquidation warning. Notification-only
// for now — auto-pausing on every margin call would be too aggressive without
// a per-user configurable threshold (see risk.Config.MaxMarginRatioPct for
// the entry-blocking side of this).
func (h *accountEventHandler) OnMarginCall(accountID uuid.UUID, ev exchange.RiskEvent) {
	slog.Warn("margin call",
		"account_id", accountID, "symbol", ev.Symbol,
		"margin_ratio", ev.MarginRatio, "liq_price", ev.LiquidationPrice)
}

// OnTradingRestricted marks the broker connection as error — the exchange
// revoking trading permission has the same practical consequence as a
// credential rejection (this connection can no longer place orders).
func (h *accountEventHandler) OnTradingRestricted(accountID uuid.UUID, reason string) {
	if err := h.brokerSvc.MarkCredentialError(context.Background(), accountID, reason); err != nil {
		slog.Error("account event: failed to mark broker connection error (trading restricted)",
			"account_id", accountID, "err", err)
	}
}

// wireAccountEventHandler wires the registry's AccountEventHandler port to the
// broker service, so account/connection-level conditions detected by a
// running HelmRuntime (credential rejection, sustained errors, margin calls,
// trading restrictions) get persisted/notified appropriately.
func wireAccountEventHandler(reg *fleet.Registry, brokerSvc service2.BrokerConnectionService) {
	var handler actor.AccountEventHandler = &accountEventHandler{brokerSvc: brokerSvc}
	reg.SetAccountEventHandler(handler)
}
