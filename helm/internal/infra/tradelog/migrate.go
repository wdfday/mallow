package tradelog

import (
	"fmt"

	"gorm.io/gorm"
)

// Migrate creates the trades table and indexes (idempotent).
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
		`CREATE INDEX IF NOT EXISTS idx_trades_hand_exit
			ON trades (hand_id, exit_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_trades_helm_exit
			ON trades (helm_id, exit_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_trades_user_exit
			ON trades (user_id, exit_at DESC)`,
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
