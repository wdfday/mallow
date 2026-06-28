package infra

import (
	"log/slog"
	"mallow/identity/internal/infra/database"
	infraNats "mallow/identity/internal/infra/nats"
	infraredis "mallow/identity/internal/infra/redis"
	"mallow/pkg/logger"

	"go.uber.org/fx"
)

// Module composes all infrastructure providers.
var Module = fx.Module("infra",
	fx.Provide(newLogger),
	// fx.Invoke(setupTracing), // TODO: re-enable when Tempo is running (just up-mon)
	database.Module,
	infraredis.Module,
	infraNats.Module,
)

func newLogger() *slog.Logger {
	return logger.New("identity")
}

// func setupTracing(lc fx.Lifecycle) error {
// 	shutdown, err := pkgtelemetry.Setup("identity")
// 	if err != nil {
// 		return err
// 	}
// 	lc.Append(fx.Hook{OnStop: shutdown})
// 	return nil
// }
