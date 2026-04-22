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

	"orchestrator/internal/config"
)

// NewDB opens a shared PostgreSQL connection. Returns (nil, nil) when POSTGRES_URL is empty.
func NewDB(cfg *config.Config, lc fx.Lifecycle) (*sql.DB, error) {
	if cfg.Infra.PostgresURL == "" {
		slog.Info("postgres disabled (POSTGRES_URL empty)")
		return nil, nil
	}

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

// NewGORMDB wraps the existing *sql.DB as a *gorm.DB.
// Returns (nil, nil) when db is nil (postgres disabled).
func NewGORMDB(db *sql.DB) (*gorm.DB, error) {
	if db == nil {
		return nil, nil
	}
	return gorm.Open(
		postgres.New(postgres.Config{Conn: db}),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)},
	)
}

func autoMigrate(db *sql.DB) error {
	stmts := []string{
		// ── orchestrators ────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS orchestrators (
			id              UUID PRIMARY KEY,
			user_id         UUID NOT NULL,
			account_id      UUID NOT NULL UNIQUE,
			name            TEXT NOT NULL,
			capital         DOUBLE PRECISION NOT NULL DEFAULT 0,
			exchange_config JSONB NOT NULL DEFAULT '{}',
			risk_config     JSONB NOT NULL DEFAULT '{}',
			enabled         BOOLEAN NOT NULL DEFAULT FALSE,
			status          TEXT NOT NULL DEFAULT 'active',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_orchestrators_user_id ON orchestrators(user_id)`,
		`ALTER TABLE orchestrators ADD COLUMN IF NOT EXISTS last_synced_at    TIMESTAMPTZ`,
		`ALTER TABLE orchestrators ADD COLUMN IF NOT EXISTS portfolio_config  JSONB NOT NULL DEFAULT '{}'`,

		// Move sizing fields from risk_config → portfolio_config (idempotent).
		`UPDATE orchestrators
			SET
				portfolio_config = portfolio_config
					|| CASE WHEN risk_config ? 'max_positions'    THEN jsonb_build_object('max_positions',    risk_config->'max_positions')    ELSE '{}'::jsonb END
					|| CASE WHEN risk_config ? 'max_position_pct' THEN jsonb_build_object('max_position_pct', risk_config->'max_position_pct') ELSE '{}'::jsonb END,
				risk_config = risk_config - 'max_positions' - 'max_position_pct'
			WHERE
				risk_config ? 'max_positions' OR risk_config ? 'max_position_pct'`,

		// ── bots ─────────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS bots (
			id              TEXT PRIMARY KEY,
			orchestrator_id UUID NOT NULL REFERENCES orchestrators(id) ON DELETE CASCADE,
			name            TEXT NOT NULL,
			type            TEXT NOT NULL DEFAULT 'signal_follower',
			market          TEXT NOT NULL DEFAULT 'spot',
			symbols         JSONB NOT NULL DEFAULT '[]',
			strategy        JSONB NOT NULL DEFAULT '{}',
			position        JSONB NOT NULL DEFAULT '{}',
			risk            JSONB NOT NULL DEFAULT '{}',
			futures         JSONB,
			status          TEXT NOT NULL DEFAULT 'stopped',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bots_orchestrator_id ON bots(orchestrator_id)`,
		`CREATE INDEX IF NOT EXISTS idx_bots_status          ON bots(status)`,

		// ── schema migrations (idempotent) ───────────────────────────────────

		// 1. Rename old 'tactic' column → 'strategy' if still present.
		`DO $$ BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name='bots' AND column_name='tactic'
			) THEN
				ALTER TABLE bots RENAME COLUMN tactic TO strategy;
			END IF;
		END $$`,

		// 2. Add new columns for bots that were created before this schema version.
		`ALTER TABLE bots ADD COLUMN IF NOT EXISTS market   TEXT NOT NULL DEFAULT 'spot'`,
		`ALTER TABLE bots ADD COLUMN IF NOT EXISTS position JSONB NOT NULL DEFAULT '{}'`,
		`ALTER TABLE bots ADD COLUMN IF NOT EXISTS futures  JSONB`,

		// 3. Move exit-rule legacy flat fields in strategy JSONB → risk JSONB.
		`UPDATE bots
			SET
				risk = risk
					|| CASE WHEN strategy ? 'stop_loss_pct'
						THEN jsonb_build_object('stop_loss_pct', (strategy->>'stop_loss_pct')::float)
						ELSE '{}'::jsonb END
					|| CASE WHEN strategy ? 'take_profit_pct'
						THEN jsonb_build_object('take_profit_pct', (strategy->>'take_profit_pct')::float)
						ELSE '{}'::jsonb END
					|| CASE WHEN strategy ? 'trailing_stop_pct'
						THEN jsonb_build_object('trailing_stop_pct', (strategy->>'trailing_stop_pct')::float)
						ELSE '{}'::jsonb END
					|| CASE WHEN strategy ? 'max_bars_held'
						THEN jsonb_build_object('max_bars_held', (strategy->>'max_bars_held')::int)
						ELSE '{}'::jsonb END,
				strategy = strategy
					- 'stop_loss_pct' - 'take_profit_pct'
					- 'trailing_stop_pct' - 'max_bars_held'
			WHERE
				strategy ? 'stop_loss_pct' OR strategy ? 'take_profit_pct'
				OR strategy ? 'trailing_stop_pct' OR strategy ? 'max_bars_held'`,

		// 4. Convert flat exit fields in risk JSONB → nested ExitConfig shape.
		//    ATR expression takes priority over fixed pct.
		`UPDATE bots
			SET risk = (
				risk
				- 'stop_loss_atr_mult' - 'take_profit_atr_mult'
				- 'stop_loss_pct'      - 'take_profit_pct' - 'max_bars_held'
				|| jsonb_build_object('exit', jsonb_strip_nulls(jsonb_build_object(
					'sl', CASE
						WHEN (risk->>'stop_loss_atr_mult')::float > 0
							THEN to_jsonb(concat((risk->>'stop_loss_atr_mult')::text, '*atr'))
						WHEN (risk->>'stop_loss_pct')::float > 0
							THEN risk->'stop_loss_pct'
						ELSE NULL::jsonb
					END,
					'tp', CASE
						WHEN (risk->>'take_profit_atr_mult')::float > 0
							THEN to_jsonb(concat((risk->>'take_profit_atr_mult')::text, '*atr'))
						WHEN (risk->>'take_profit_pct')::float > 0
							THEN risk->'take_profit_pct'
						ELSE NULL::jsonb
					END,
					'max_bars', CASE
						WHEN (risk->>'max_bars_held')::int > 0 THEN risk->'max_bars_held'
						ELSE NULL::jsonb
					END
				)))
			)
			WHERE
				risk ? 'stop_loss_atr_mult' OR risk ? 'take_profit_atr_mult'
				OR risk ? 'stop_loss_pct'   OR risk ? 'take_profit_pct'
				OR risk ? 'max_bars_held'`,

		// 5. Migrate sizing fields from risk JSONB → position JSONB.
		`UPDATE bots
			SET
				position = position
					|| CASE WHEN risk ? 'allocated_capital'  THEN jsonb_build_object('allocated_capital',  risk->'allocated_capital')  ELSE '{}'::jsonb END
					|| CASE WHEN risk ? 'allocated_pct'      THEN jsonb_build_object('allocated_pct',      risk->'allocated_pct')      ELSE '{}'::jsonb END
					|| CASE WHEN risk ? 'unit_capital'       THEN jsonb_build_object('unit_capital',       risk->'unit_capital')       ELSE '{}'::jsonb END
					|| CASE WHEN risk ? 'unit_pct'           THEN jsonb_build_object('unit_pct',           risk->'unit_pct')           ELSE '{}'::jsonb END
					|| CASE WHEN risk ? 'fixed_qty'          THEN jsonb_build_object('fixed_qty',          risk->'fixed_qty')          ELSE '{}'::jsonb END
					|| CASE WHEN risk ? 'max_positions'      THEN jsonb_build_object('max_positions',      risk->'max_positions')      ELSE '{}'::jsonb END
					|| CASE WHEN risk ? 'size_mode'          THEN jsonb_build_object('size_mode',          risk->'size_mode')          ELSE '{}'::jsonb END
					|| CASE WHEN risk ? 'risk_per_trade_pct' THEN jsonb_build_object('risk_per_trade_pct', risk->'risk_per_trade_pct') ELSE '{}'::jsonb END
					|| CASE WHEN risk ? 'max_position_pct'   THEN jsonb_build_object('max_position_pct',   risk->'max_position_pct')   ELSE '{}'::jsonb END,
				risk = risk
					- 'allocated_capital' - 'allocated_pct'
					- 'unit_capital'      - 'unit_pct'      - 'fixed_qty'
					- 'max_positions'     - 'size_mode'
					- 'risk_per_trade_pct'- 'max_position_pct'
			WHERE
				risk ? 'allocated_capital' OR risk ? 'unit_capital'
				OR risk ? 'max_positions'  OR risk ? 'size_mode'
				OR risk ? 'fixed_qty'`,
	}

	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("postgres automigrate: %w", err)
		}
	}
	return nil
}
