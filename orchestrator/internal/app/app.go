package app

import (
	"go.uber.org/fx"
)

// New builds the fx application with all modules (config, infra, portfolio, engine, risk, bot, tick, api, lifecycle).
func New() *fx.App {
	return fx.New(Module)
}
