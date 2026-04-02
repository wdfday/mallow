package redis

import (
	"context"
	"fmt"
	"log/slog"
	"mallow/identity/internal/config"

	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
)

// Module provides redis client to fx.
var Module = fx.Module("redis", fx.Provide(New))

// New creates a Redis client from config.
func New(cfg *config.Config, logger *slog.Logger) (*redis.Client, error) {
	opt, err := redis.ParseURL(cfg.Redis.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_URL: %w", err)
	}

	client := redis.NewClient(opt)

	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	logger.Info("Connected to Redis", "url", cfg.Redis.URL)
	return client, nil
}
