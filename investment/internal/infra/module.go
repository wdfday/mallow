package infra

import (
	"log/slog"

	"go.uber.org/fx"

	"mallow/investment/internal/infra/database"
	natsinfra "mallow/investment/internal/infra/nats"
	pgxinfra "mallow/investment/internal/infra/pgx"
	"mallow/pkg/logger"
)

var Module = fx.Module("infra",
	fx.Provide(newLogger),
	database.Module,
	pgxinfra.Module,
	natsinfra.Module,
	fx.Invoke(provideTracerProvider),
)

func newLogger() *slog.Logger {
	return logger.New("investment")
}
