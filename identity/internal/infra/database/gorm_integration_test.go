//go:build integration

package database

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/config"
)

func TestNew_ConnectsAndMigrates(t *testing.T) {
	// config.Load() reads .env file automatically via Viper — no env var juggling needed.
	cfg := config.Load()
	cfg.Server.GinMode = "release"
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	db, err := New(cfg, logger)
	if err != nil {
		t.Skipf("Postgres unreachable (%v) — run: docker compose -f identity/docker-compose.yml up -d", err)
	}
	require.NotNil(t, db)

	assert.True(t, db.Migrator().HasTable("users"), "users table should exist after AutoMigrate")
	assert.True(t, db.Migrator().HasTable("user_profiles"), "user_profiles table should exist after AutoMigrate")

	sqlDB, _ := db.DB()
	sqlDB.Close()
}

func TestNew_BadDSN_ReturnsError(t *testing.T) {
	cfg := config.Load()
	cfg.Database.URL = "postgres://identity:wrong@localhost:9999/nodb?sslmode=disable"
	cfg.Server.GinMode = "release"
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	db, err := New(cfg, logger)
	assert.Error(t, err)
	assert.Nil(t, db)
}
