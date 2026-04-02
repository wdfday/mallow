package user

import (
	"mallow/identity/internal/module/user/handler"
	"mallow/identity/internal/module/user/repository"
	"mallow/identity/internal/module/user/service"

	"go.uber.org/fx"
)

// Module provides user module dependencies
var Module = fx.Module("user",
	fx.Provide(
		// Repository - provide as interface
		fx.Annotate(
			repository.New, // Using gorm repository
			fx.As(new(repository.Repository)),
		),

		// Service - provide as interface
		fx.Annotate(
			service.NewUserService,
			fx.As(new(service.IUserService)),
		),

		// Handler
		handler.NewHandler,
	),
)
