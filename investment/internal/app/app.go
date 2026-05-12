package app

import (
	"fmt"

	"go.uber.org/fx"

	"mallow/investment/internal/config"
	"mallow/investment/internal/infra"
	"mallow/investment/internal/middleware"
	accountModule "mallow/investment/internal/module/account"
	brokerModule "mallow/investment/internal/module/broker"
	cashFlowModule "mallow/investment/internal/module/cash_flow"
	derivativeModule "mallow/investment/internal/module/derivative"
	positionModule "mallow/investment/internal/module/position"
	snapshotModule "mallow/investment/internal/module/snapshot"
	transactionModule "mallow/investment/internal/module/transaction"
	"mallow/investment/internal/portfolio"
	"mallow/investment/internal/router"
	"mallow/investment/internal/server"
	internalService "mallow/investment/internal/service"
)

// provideEncryptionService creates the shared AES-256-GCM encryption service.
func provideEncryptionService(cfg *config.Config) (*internalService.EncryptionService, error) {
	svc, err := internalService.NewEncryptionService(cfg.Encryption.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to create encryption service: %w", err)
	}
	return svc, nil
}

// New builds and returns the fully-wired fx.App.
func New() *fx.App {
	return fx.New(
		config.Module,
		infra.Module,
		server.Module,

		// Shared encryption service (used by broker + account modules)
		fx.Provide(provideEncryptionService),

		// Event-sourcing core (aggregate, store, projections, consumer, publisher, replay)
		portfolio.Module,

		// Auth middleware (shared by broker/account handlers)
		fx.Provide(middleware.New),

		// Read-model modules
		positionModule.Module,
		transactionModule.Module,
		derivativeModule.Module,
		cashFlowModule.Module,
		snapshotModule.Module,

		// Broker connections + financial accounts
		brokerModule.Module,
		accountModule.Module,

		router.Module,
	)
}
