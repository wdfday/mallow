package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/fx"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"mallow/helm/internal/config"
)

// NewDB opens a shared PostgreSQL connection.
// Returns an error when POSTGRES_URL is not set — Postgres is required (no in-memory fallback).
func NewDB(cfg *config.Config, lc fx.Lifecycle) (*sql.DB, error) {
	if cfg.Infra.PostgresURL == "" {
		return nil, fmt.Errorf("POSTGRES_URL is required — helm service has no in-memory fallback")
	}

	db, err := sql.Open("pgx", cfg.Infra.PostgresURL)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	slog.Info("postgres connected")

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			slog.Info("closing postgres connection")
			return db.Close()
		},
	})
	return db, nil
}

// NewGORMDB wraps the existing *sql.DB as a *gorm.DB.
func NewGORMDB(db *sql.DB) (*gorm.DB, error) {
	return gorm.Open(
		postgres.New(postgres.Config{Conn: db}),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)},
	)
}
