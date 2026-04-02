//go:build integration

package redis

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/config"
)

func TestNew_ConnectsToRedis(t *testing.T) {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	client, err := New(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, client)
	defer client.Close()

	ctx := context.Background()
	err = client.Set(ctx, "test:integration:key", "hello", 5*time.Second).Err()
	require.NoError(t, err)

	val, err := client.Get(ctx, "test:integration:key").Result()
	require.NoError(t, err)
	assert.Equal(t, "hello", val)

	client.Del(ctx, "test:integration:key")
}

func TestNew_BadRedisURL_ReturnsError(t *testing.T) {
	cfg := config.Load()
	cfg.Redis.URL = "redis://localhost:9999"
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	client, err := New(cfg, logger)
	assert.Error(t, err)
	assert.Nil(t, client)
}

func TestNew_InvalidURL_ParseError(t *testing.T) {
	cfg := config.Load()
	cfg.Redis.URL = "not-a-url"
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	client, err := New(cfg, logger)
	assert.Error(t, err)
	assert.Nil(t, client)
}

func TestRedis_Expiry(t *testing.T) {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	client, err := New(cfg, logger)
	require.NoError(t, err)
	defer client.Close()

	ctx := context.Background()
	key := "test:integration:expiry"

	err = client.Set(ctx, key, "expire-me", 1*time.Second).Err()
	require.NoError(t, err)

	time.Sleep(1100 * time.Millisecond)

	_, err = client.Get(ctx, key).Result()
	assert.ErrorIs(t, err, goredis.Nil)
}
