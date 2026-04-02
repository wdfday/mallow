package snapshot

import (
	"log/slog"

	"go.uber.org/fx"

	"mallow/investment/internal/module/portfolio/command"
	posSvc "mallow/investment/internal/module/position/service"
	"mallow/investment/internal/module/snapshot/handler"
	"mallow/investment/internal/module/snapshot/repository"
	"mallow/investment/internal/module/snapshot/service"
)

var Module = fx.Module("snapshot",
	fx.Provide(
		fx.Annotate(repository.NewPgx, fx.As(new(repository.Repository))),
		// Snapshot service needs positionQuerier (internal interface);
		// posSvc.Service satisfies it via ListActiveByAccount.
		func(repo repository.Repository, h *command.Handler, pq posSvc.Service, log *slog.Logger) service.Service {
			return service.New(repo, h, pq, log)
		},
		handler.New,
	),
)
