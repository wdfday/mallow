package account

import (
	"mallow/helm/internal/module/account/repository"
	"mallow/helm/internal/module/account/service"

	"go.uber.org/fx"
)

// Module provides account module dependencies.
// Handler is constructed in app/fx.go so it can be wired with helm/hand services.
var Module = fx.Module("account",
	fx.Provide(
		// Repository - provide as interface
		fx.Annotate(
			repository.New,
			fx.As(new(repository.Repository)),
		),

		// Service - provide as interface
		fx.Annotate(
			service.NewService,
			fx.As(new(service.Service)),
		),
	),
)
