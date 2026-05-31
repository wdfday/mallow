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
			status          TEXT NOT NULL DEFAULT 'disabled',
			created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_helms_user_id ON helms(user_id)`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS last_synced_at    TIMESTAMPTZ`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS broker_type       TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS account_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE helms DROP COLUMN IF EXISTS enabled`,
		// PortfolioConfig was folded into RiskConfig: max_positions moves back into
		// risk_config; max_position_pct and reserve_ratio are dropped (dead config).
		// ADD then DROP keeps this idempotent on fresh DBs (no portfolio_config column).
		`ALTER TABLE helms ADD COLUMN IF NOT EXISTS portfolio_config JSONB NOT NULL DEFAULT '{}'`,
		`UPDATE helms
			SET risk_config = risk_config
				|| CASE WHEN portfolio_config ? 'max_positions'
					THEN jsonb_build_object('max_positions', portfolio_config->'max_positions')
					ELSE '{}'::jsonb END
			WHERE portfolio_config ? 'max_positions'`,
		`ALTER TABLE helms DROP COLUMN IF EXISTS portfolio_config`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("helm migrate: %w", err)
		}
	}
	return nil
}
