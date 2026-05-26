package perflog

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// MigrateFills creates the fills table (idempotent).
func MigrateFills(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS fills (
			id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
			trade_id    TEXT        NOT NULL,
			order_id    TEXT,
			helm_id     UUID        NOT NULL,
			account_id  UUID        NOT NULL,
			user_id     UUID        NOT NULL,
			hand_id     UUID,
			symbol      TEXT        NOT NULL,
			side        TEXT        NOT NULL,
			kind        TEXT        NOT NULL DEFAULT 'fill',
			qty         NUMERIC(20,8),
			avg_price   NUMERIC(20,8),
			fee         NUMERIC(20,8),
			filled_at   TIMESTAMPTZ NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_fills_trade_id ON fills (trade_id)`,
		`CREATE INDEX IF NOT EXISTS idx_fills_account_filled ON fills (account_id, filled_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_fills_hand_filled ON fills (hand_id, filled_at DESC) WHERE hand_id IS NOT NULL`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("fills migrate: %w", err)
			}
		}
	}
	return nil
}
