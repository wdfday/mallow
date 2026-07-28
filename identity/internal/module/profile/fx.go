package profile

import (
	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/module/profile/handler"
	"mallow/identity/internal/module/profile/repository"
	"mallow/identity/internal/module/profile/service"

	"go.uber.org/fx"
)

// Module provides profile module dependencies.
var Module = fx.Module("profile",
	fx.Provide(
		fx.Annotate(
			repository.New,
			fx.As(new(domain.Repository)),
		),
		service.NewService,
		handler.NewHandler,
	),
)
