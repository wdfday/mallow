package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.uber.org/fx"

	"orchestrator/internal/config"
)

// NewDB opens a shared PostgreSQL connection from config. Optional: when PostgresURL is empty returns (nil, nil).
// Callers that need DB should check for nil before use. Registers Close on fx lifecycle when non-nil.
func NewDB(cfg *config.Config, lc fx.Lifecycle) (*sql.DB, error) {
	if cfg.Infra.PostgresURL == "" {
		slog.Info("postgres disabled (POSTGRES_URL empty)")
		return nil, nil
	}

	// Prefer pgx driver if available; otherwise use lib/pq. We use pgx stdlib for database/sql.
	db, err := sql.Open("pgx", cfg.Infra.PostgresURL)
	if err != nil {
		return nil, err
	}

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := autoMigrate(db); err != nil {
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

func autoMigrate(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS orchestrators (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL,
			account_id UUID NOT NULL UNIQUE,
			name TEXT NOT NULL,
			capital DOUBLE PRECISION NOT NULL DEFAULT 0,
			exchange_config JSONB NOT NULL DEFAULT '{}',
			risk_config JSONB NOT NULL DEFAULT '{}',
			enabled BOOLEAN NOT NULL DEFAULT FALSE,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_orchestrators_user_id ON orchestrators(user_id)`,
		`ALTER TABLE orchestrators ADD COLUMN IF NOT EXISTS last_synced_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS bots (
			id TEXT PRIMARY KEY,
			orchestrator_id UUID NOT NULL REFERENCES orchestrators(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'signal_follower',
			symbols JSONB NOT NULL DEFAULT '[]',
			tactic JSONB NOT NULL DEFAULT '{}',
			risk JSONB NOT NULL DEFAULT '{}',
			status TEXT NOT NULL DEFAULT 'stopped',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bots_orchestrator_id ON bots(orchestrator_id)`,
		`CREATE INDEX IF NOT EXISTS idx_bots_status ON bots(status)`,
		// Rename legacy 'strategy' column to 'tactic' if it still exists.
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name='bots' AND column_name='strategy'
			) THEN
				ALTER TABLE bots RENAME COLUMN strategy TO tactic;
			END IF;
		END $$`,
		// Migrate exit-risk fields from tactic JSONB to risk JSONB (one-time, idempotent).
		// Copies stop_loss_pct / take_profit_pct / trailing_stop_pct / max_bars_held
		// out of the tactic column and into the risk column, then removes them from tactic.
		`UPDATE bots
			SET
				risk = risk
					|| CASE WHEN tactic ? 'stop_loss_pct'     THEN jsonb_build_object('stop_loss_pct',     (tactic->>'stop_loss_pct')::float)     ELSE '{}'::jsonb END
					|| CASE WHEN tactic ? 'take_profit_pct'   THEN jsonb_build_object('take_profit_pct',   (tactic->>'take_profit_pct')::float)   ELSE '{}'::jsonb END
					|| CASE WHEN tactic ? 'trailing_stop_pct' THEN jsonb_build_object('trailing_stop_pct', (tactic->>'trailing_stop_pct')::float) ELSE '{}'::jsonb END
					|| CASE WHEN tactic ? 'max_bars_held'     THEN jsonb_build_object('max_bars_held',     (tactic->>'max_bars_held')::int)       ELSE '{}'::jsonb END,
				tactic = tactic
					- 'stop_loss_pct'
					- 'take_profit_pct'
					- 'trailing_stop_pct'
					- 'max_bars_held'
			WHERE
				tactic ? 'stop_loss_pct'
				OR tactic ? 'take_profit_pct'
				OR tactic ? 'trailing_stop_pct'
				OR tactic ? 'max_bars_held'`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("postgres automigrate: %w", err)
		}
	}

	return nil
}
