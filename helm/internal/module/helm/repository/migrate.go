package repository

import (
	"fmt"

	"gorm.io/gorm"
)

// Migrate runs idempotent DDL + data migrations for the helms table.
func Migrate(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS helms (
			id              UUID PRIMARY KEY,
			user_id         UUID NOT NULL,
			account_id      UUID NOT NULL UNIQUE,
			name            TEXT NOT NULL,
			risk_config     JSONB NOT NULL DEFAULT '{}',
			enabled         BOOLEAN NOT NULL DEFAULT FALSE,
			status          TEXT NOT NULL DEFAULT 'active',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_helms_user_id ON helms(user_id)`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS last_synced_at    TIMESTAMPTZ`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS portfolio_config  JSONB NOT NULL DEFAULT '{}'`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS broker_type       TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS account_type TEXT NOT NULL DEFAULT ''`,
		// Move sizing fields from risk_config → portfolio_config (idempotent).
		`UPDATE helms
			SET
				portfolio_config = portfolio_config
					|| CASE WHEN risk_config ? 'max_positions'    THEN jsonb_build_object('max_positions',    risk_config->'max_positions')    ELSE '{}'::jsonb END
					|| CASE WHEN risk_config ? 'max_position_pct' THEN jsonb_build_object('max_position_pct', risk_config->'max_position_pct') ELSE '{}'::jsonb END,
				risk_config = risk_config - 'max_positions' - 'max_position_pct'
			WHERE
				risk_config ? 'max_positions' OR risk_config ? 'max_position_pct'`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("helm migrate: %w", err)
		}
	}
	return nil
}
