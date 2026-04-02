package pgxinfra

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"mallow/investment/internal/config"
)

var Module = fx.Module("pgx", fx.Provide(New))

func New(lc fx.Lifecycle, cfg *config.Config, logger *slog.Logger) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(context.Background(), cfg.Database.URL)
	if err != nil {
		return nil, err
	}

	// Verify connectivity
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, err
	}

	logger.Info("Connected to PostgreSQL via pgxpool (investment)")

	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			pool.Close()
			return nil
		},
	})

	return pool, nil
}
