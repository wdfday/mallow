package tradelog

import (
	"fmt"

	"gorm.io/gorm"
)

// Migrate creates the trades table and indexes (idempotent).
//
// Column semantics:
//
//	pnl              — gross realized PnL = (exit_price − entry_price) × qty (no fees)
//	commission       — total fees on entry + exit (always non-negative)
//	net_pnl          — generated: pnl − commission (use this for KPI math)
//	holding_seconds  — generated: exit_at − entry_at (NULL when entry_at is NULL)
//	source           — exit reason: signal / sl / tp / kill / bracket_exit / external
//	pattern_kind     — technical pattern that triggered entry (e.g. "bull_flag"); nullable
//	regime_state     — market regime tag at entry (e.g. "trend_up"); nullable
func Migrate(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS trades (
			id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
			hand_id      UUID         NOT NULL,
			helm_id      UUID         NOT NULL,
			user_id      UUID         NOT NULL,
			symbol       TEXT         NOT NULL,
			side         TEXT         NOT NULL,
			entry_price  NUMERIC(20,8),
			exit_price   NUMERIC(20,8),
			qty          NUMERIC(20,8),
			pnl          NUMERIC(20,8),
			commission   NUMERIC(20,8) NOT NULL DEFAULT 0,
			source       TEXT,
			strategy     TEXT,
			entry_at     TIMESTAMPTZ,
			exit_at      TIMESTAMPTZ  NOT NULL,
			created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		)`,
		// Additive columns introduced for KPI / attribution. ALTER … IF NOT EXISTS
		// keeps the migration idempotent.
		`ALTER TABLE trades
			ADD COLUMN IF NOT EXISTS pattern_kind      TEXT,
			ADD COLUMN IF NOT EXISTS regime_state      TEXT,
			ADD COLUMN IF NOT EXISTS timeframe         TEXT,
			ADD COLUMN IF NOT EXISTS stop_loss_price   NUMERIC(20,8),
			ADD COLUMN IF NOT EXISTS take_profit_price NUMERIC(20,8),
			ADD COLUMN IF NOT EXISTS planned_risk      NUMERIC(20,8),
			ADD COLUMN IF NOT EXISTS entry_order_id    TEXT,
			ADD COLUMN IF NOT EXISTS exit_order_id     TEXT,
			ADD COLUMN IF NOT EXISTS n_entries         INT`,
		`ALTER TABLE trades
			ADD COLUMN IF NOT EXISTS net_pnl NUMERIC(20,8)
				GENERATED ALWAYS AS (COALESCE(pnl, 0) - COALESCE(commission, 0)) STORED`,
		`ALTER TABLE trades
			ADD COLUMN IF NOT EXISTS holding_seconds INT
				GENERATED ALWAYS AS (
					CASE WHEN entry_at IS NULL THEN NULL
					     ELSE EXTRACT(EPOCH FROM (exit_at - entry_at))::INT
					END
				) STORED`,
		// r_multiple = net_pnl / planned_risk — NULL when planned_risk is missing or zero.
		// Lets KPI queries filter "trades > 2R", "average R-multiple", "expectancy in R" trivially.
		`ALTER TABLE trades
			ADD COLUMN IF NOT EXISTS r_multiple NUMERIC(10,4)
				GENERATED ALWAYS AS (
					CASE WHEN planned_risk IS NULL OR planned_risk = 0 THEN NULL
					     ELSE (COALESCE(pnl, 0) - COALESCE(commission, 0)) / planned_risk
					END
				) STORED`,
		`CREATE INDEX IF NOT EXISTS idx_trades_hand_exit
			ON trades (hand_id, exit_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_trades_helm_exit
			ON trades (helm_id, exit_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_trades_user_exit
			ON trades (user_id, exit_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_trades_strategy_exit
			ON trades (strategy, exit_at DESC) WHERE strategy IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_trades_pattern_exit
			ON trades (pattern_kind, exit_at DESC) WHERE pattern_kind IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_trades_timeframe_exit
			ON trades (timeframe, exit_at DESC) WHERE timeframe IS NOT NULL`,
		// Dedup index: same hand + entry + exit should not produce two rows.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_trades_dedup
			ON trades (hand_id, entry_at, exit_at)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("tradelog migrate: %w", err)
		}
	}
	return nil
}
