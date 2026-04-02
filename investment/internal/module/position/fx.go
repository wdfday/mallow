package position

import (
	"go.uber.org/fx"

	"mallow/investment/internal/module/position/handler"
	"mallow/investment/internal/module/position/repository"
	"mallow/investment/internal/module/position/service"
)

var Module = fx.Module("position",
	fx.Provide(
		fx.Annotate(repository.NewPgx, fx.As(new(repository.Repository))),
		service.New,
		handler.New,
	),
)
