package signallog

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Migrate creates the signals table (idempotent).
func Migrate(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS signals (
			id           UUID             PRIMARY KEY,
			helm_id      UUID             NOT NULL,
			hand_id      UUID             NOT NULL,
			user_id      UUID             NOT NULL,
			symbol       TEXT             NOT NULL,
			direction    TEXT             NOT NULL,
			exit_kind    TEXT,
			strength     DOUBLE PRECISION,
			price        NUMERIC(20,8),
			target_price NUMERIC(20,8),
			stop_price   NUMERIC(20,8),
			is_offset    BOOLEAN          NOT NULL DEFAULT FALSE,
			atr          NUMERIC(20,8),
			reason       TEXT,
			generated_at TIMESTAMPTZ,
			received_at  TIMESTAMPTZ,
			created_at   TIMESTAMPTZ      NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_signals_hand_generated ON signals (hand_id, generated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_signals_helm_generated ON signals (helm_id, generated_at DESC)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("signals migrate: %w", err)
			}
		}
	}
	return nil
}
