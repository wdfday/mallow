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
func NewGORMDB(db *sql.DB) (*gorm.DB, error) {
	return gorm.Open(
		postgres.New(postgres.Config{Conn: db}),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)},
	)
}

func autoMigrate(db *sql.DB) error {
	stmts := []string{
		// ── helms ────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS helms (
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
		`CREATE INDEX IF NOT EXISTS idx_helms_user_id ON helms(user_id)`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS last_synced_at    TIMESTAMPTZ`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS portfolio_config  JSONB NOT NULL DEFAULT '{}'`,

		// Strip broker credentials from exchange_config — credentials are now fetched
		// on demand from the investment service and must not be persisted in helm's DB.
		`UPDATE helms
			SET exchange_config = exchange_config - 'api_key' - 'api_secret' - 'passphrase'
			WHERE exchange_config ? 'api_key' OR exchange_config ? 'api_secret' OR exchange_config ? 'passphrase'`,

		// Move sizing fields from risk_config → portfolio_config (idempotent).
		`UPDATE helms
			SET
				portfolio_config = portfolio_config
					|| CASE WHEN risk_config ? 'max_positions'    THEN jsonb_build_object('max_positions',    risk_config->'max_positions')    ELSE '{}'::jsonb END
					|| CASE WHEN risk_config ? 'max_position_pct' THEN jsonb_build_object('max_position_pct', risk_config->'max_position_pct') ELSE '{}'::jsonb END,
				risk_config = risk_config - 'max_positions' - 'max_position_pct'
			WHERE
				risk_config ? 'max_positions' OR risk_config ? 'max_position_pct'`,

		// ── hands ─────────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS hands (
			id              TEXT PRIMARY KEY,
			helm_id UUID NOT NULL REFERENCES helms(id) ON DELETE CASCADE,
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
		`CREATE INDEX IF NOT EXISTS idx_hands_helm_id ON hands(helm_id)`,
		`CREATE INDEX IF NOT EXISTS idx_hands_status          ON hands(status)`,

		// 2. Add new columns for hands that were created before this schema version.
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS market   TEXT NOT NULL DEFAULT 'spot'`,
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS position JSONB NOT NULL DEFAULT '{}'`,
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS futures  JSONB`,

		// 3. Move exit-rule legacy flat fields in strategy JSONB → risk JSONB.
		`UPDATE hands
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
		`UPDATE hands
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
		`UPDATE hands
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
