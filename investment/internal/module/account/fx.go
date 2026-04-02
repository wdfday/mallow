package account

import (
	"mallow/investment/internal/module/account/handler"
	"mallow/investment/internal/module/account/repository"
	"mallow/investment/internal/module/account/service"

	"go.uber.org/fx"
)

// Module provides account module dependencies.
var Module = fx.Module("account",
	fx.Provide(
		// Repository - provide as interface (không dùng name; accountRepo vs transactionRepo khác type nên Fx phân biệt được)
		fx.Annotate(
			repository.New,
			fx.As(new(repository.Repository)),
		),

		// Service - provide as interface
		fx.Annotate(
			service.NewService,
			fx.As(new(service.Service)),
		),

		// Handler
		handler.NewHandler,
	),
)
