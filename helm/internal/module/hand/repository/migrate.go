package repository

import (
	"fmt"

	"gorm.io/gorm"
)

// Migrate runs idempotent DDL + data migrations for the hands table.
func Migrate(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS hands (
			id              TEXT PRIMARY KEY,
			helm_id         UUID NOT NULL REFERENCES helms(id) ON DELETE CASCADE,
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
		`CREATE INDEX IF NOT EXISTS idx_hands_status  ON hands(status)`,
		// Add columns for hands created before this schema version.
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS market   TEXT NOT NULL DEFAULT 'spot'`,
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS position JSONB NOT NULL DEFAULT '{}'`,
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS futures  JSONB`,
		// Move exit-rule legacy flat fields: strategy JSONB → risk JSONB.
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
		// Convert flat exit fields in risk JSONB → nested ExitConfig shape.
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
		// Migrate sizing fields: risk JSONB → position JSONB.
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
		// Promote allocated_capital and signal_ttl_sec: position JSONB → dedicated columns.
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS allocated_capital NUMERIC(20,8) NOT NULL DEFAULT 0`,
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS signal_ttl_sec    INTEGER       NOT NULL DEFAULT 0`,
		`UPDATE hands
			SET
				allocated_capital = COALESCE((position->>'allocated_capital')::numeric, 0),
				signal_ttl_sec    = COALESCE((position->>'signal_ttl_sec')::int, 0)
			WHERE
				position ? 'allocated_capital' OR position ? 'signal_ttl_sec'`,
		`UPDATE hands
			SET position = position - 'allocated_capital' - 'signal_ttl_sec'
			WHERE position ? 'allocated_capital' OR position ? 'signal_ttl_sec'`,
		// Promote limit-order fields: position JSONB → dedicated columns.
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS order_type        TEXT NOT NULL DEFAULT 'market'`,
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS limit_timeout_sec INTEGER      NOT NULL DEFAULT 0`,
		`ALTER TABLE hands ADD COLUMN IF NOT EXISTS limit_fallback    TEXT         NOT NULL DEFAULT 'cancel'`,
		`UPDATE hands
			SET
				order_type        = COALESCE(position->>'order_type',        'market'),
				limit_timeout_sec = COALESCE((position->>'limit_timeout_sec')::int, 0),
				limit_fallback    = COALESCE(position->>'limit_fallback',    'cancel')
			WHERE
				position ? 'order_type' OR position ? 'limit_timeout_sec' OR position ? 'limit_fallback'`,
		`UPDATE hands
			SET position = position - 'order_type' - 'limit_timeout_sec' - 'limit_fallback'
			WHERE position ? 'order_type' OR position ? 'limit_timeout_sec' OR position ? 'limit_fallback'`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("hand migrate: %w", err)
		}
	}
	return nil
}
