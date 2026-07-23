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
	// database/sql defaults to MaxOpenConns=0 (unlimited) and MaxIdleConns=2.
	// Unlimited is a real risk under load: the shared Postgres container's own
	// max_connections is 100 (default, unconfigured — see deployment/docker-compose.yml),
	// split across identity/herald/helm/exporters. helm gets the largest share
	// since it has the most concurrent DB-touching goroutines (poslog/tradelog/
	// fillog/eventlog/signallog persisters, one trade actor per helm). 50 leaves
	// headroom for the other services within the 100 total; MaxIdleConns matches
	// it to avoid connection churn under steady load (default of 2 would mean
	// constant open/close under any real concurrency).
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(50)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	slog.Info("postgres connected", "max_open_conns", 50, "max_idle_conns", 50)

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
