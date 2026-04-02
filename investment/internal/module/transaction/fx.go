package transaction

import (
	"go.uber.org/fx"

	"mallow/investment/internal/module/transaction/handler"
	"mallow/investment/internal/module/transaction/repository"
	"mallow/investment/internal/module/transaction/service"
)

var Module = fx.Module("transaction",
	fx.Provide(
		fx.Annotate(repository.NewPgx, fx.As(new(repository.Repository))),
		service.New,
		handler.New,
	),
)
