package watchlist

import (
	"go.uber.org/fx"

	"mallow/investment/internal/module/watchlist/handler"
	"mallow/investment/internal/module/watchlist/repository"
	"mallow/investment/internal/module/watchlist/service"
)

var Module = fx.Module("watchlist",
	fx.Provide(
		fx.Annotate(repository.NewPgx, fx.As(new(repository.Repository))),
		service.New,
		handler.New,
	),
)
