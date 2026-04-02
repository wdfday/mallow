package derivative

import (
	"go.uber.org/fx"

	"mallow/investment/internal/module/derivative/handler"
	"mallow/investment/internal/module/derivative/repository"
	"mallow/investment/internal/module/derivative/service"
)

var Module = fx.Module("derivative",
	fx.Provide(
		fx.Annotate(repository.NewPgx, fx.As(new(repository.Repository))),
		service.New,
		handler.New,
	),
)
