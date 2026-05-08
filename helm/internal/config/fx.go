package config

import "go.uber.org/fx"

// Module provides Config to fx (pointer so multiple components can share).
var Module = fx.Module("config",
	fx.Provide(func() *Config {
		c := Load()
		return &c
	}),
)
