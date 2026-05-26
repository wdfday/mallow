package perflog

import (
	"fmt"

	"gorm.io/gorm"
)

// MigrateSnapshots creates the equity_snapshots table and indexes (idempotent).
func MigrateSnapshots(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS equity_snapshots (
			id             UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
			helm_id        UUID         NOT NULL,
			hand_id        UUID,
			ts             TIMESTAMPTZ  NOT NULL,
			cash           NUMERIC(20,8) NOT NULL DEFAULT 0,
			equity         NUMERIC(20,8) NOT NULL DEFAULT 0,
			realized_pnl   NUMERIC(20,8) NOT NULL DEFAULT 0,
			unrealized_pnl NUMERIC(20,8) NOT NULL DEFAULT 0,
			positions      JSONB        NOT NULL DEFAULT '[]',
			created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		)`,
		// Dedup: one snapshot per entity per millisecond.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_equity_snapshots_dedup
			ON equity_snapshots (helm_id, COALESCE(hand_id, '00000000-0000-0000-0000-000000000000'), ts)`,
		`CREATE INDEX IF NOT EXISTS idx_equity_snapshots_helm_ts
			ON equity_snapshots (helm_id, ts DESC) WHERE hand_id IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_equity_snapshots_hand_ts
			ON equity_snapshots (helm_id, hand_id, ts DESC) WHERE hand_id IS NOT NULL`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("equity_snapshots migrate: %w", err)
		}
	}
	return nil
}
