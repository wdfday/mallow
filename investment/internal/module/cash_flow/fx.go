package cash_flow

import (
	"go.uber.org/fx"

	"mallow/investment/internal/module/cash_flow/handler"
	"mallow/investment/internal/module/cash_flow/repository"
	"mallow/investment/internal/module/cash_flow/service"
)

var Module = fx.Module("cash_flow",
	fx.Provide(
		fx.Annotate(repository.NewPgx, fx.As(new(repository.Repository))),
		service.New,
		handler.New,
	),
)
