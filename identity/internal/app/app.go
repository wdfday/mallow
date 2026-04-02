package app

import (
	"mallow/identity/internal/config"
	"mallow/identity/internal/infra"
	"mallow/identity/internal/router"
	"mallow/identity/internal/seed"
	"mallow/identity/internal/server"

	authModule "mallow/identity/internal/module/auth"
	notificationModule "mallow/identity/internal/module/notification"
	profileModule "mallow/identity/internal/module/profile"
	userModule "mallow/identity/internal/module/user"
	"mallow/identity/internal/telegram"

	"go.uber.org/fx"
)

// New builds the fx application with all modules.
// Infra: config, DB, Redis, NATS.
// Core: server (Gin), router (central route registration — one place for middleware order and route wiring).
// Feature modules: auth, user, profile, notification.
// Telegram is kept as a top-level module (not under infra): it's an application component (bot + /link flow),
// not a generic client like DB/Redis/NATS. Optional: move to internal/infra/telegram if you prefer grouping runnable clients.
func New() *fx.App {
	return fx.New(
		config.Module,
		infra.Module,
		server.Module,
		router.Module,

		authModule.Module,
		userModule.Module,
		profileModule.Module,
		notificationModule.Module,
		seed.Module,
		telegram.Module,
	)
}
