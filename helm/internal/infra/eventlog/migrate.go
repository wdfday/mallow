package eventlog

import (
	"fmt"

	"gorm.io/gorm"
)

// Migrate creates the helm_events table and indexes (idempotent).
func Migrate(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS helm_events (
			id        BIGSERIAL    PRIMARY KEY,
			at        TIMESTAMPTZ  NOT NULL,
			helm_id   UUID         NOT NULL,
			hand_id   UUID,
			user_id   UUID         NOT NULL,
			code      INT          NOT NULL,
			symbol    TEXT,
			direction TEXT,
			side      TEXT,
			qty       TEXT,
			price     TEXT,
			order_id  TEXT,
			reason    TEXT,
			msg       TEXT
		)`,
		// Composite index for per-helm history queries (most common).
		`CREATE INDEX IF NOT EXISTS idx_helm_events_helm_at
			ON helm_events (helm_id, at DESC)`,
		// Partial index for per-hand queries.
		`CREATE INDEX IF NOT EXISTS idx_helm_events_hand_at
			ON helm_events (hand_id, at DESC)
			WHERE hand_id IS NOT NULL`,
		// Drop the old dedup index (helm_id, code, at) which did not include
		// hand_id — two hands emitting the same code in the same microsecond
		// under the same helm would cause a spurious conflict.
		`DROP INDEX IF EXISTS idx_helm_events_dedup`,
		// New dedup index matches the JetStream MsgID (helmID+handID+code+ts-ms).
		// COALESCE maps NULL hand_id to the nil UUID so the expression index
		// fires for both hand-level and helm-level events.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_helm_events_dedup2
			ON helm_events (helm_id, COALESCE(hand_id, '00000000-0000-0000-0000-000000000000'), code, at)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			return fmt.Errorf("eventlog migrate: %w", err)
		}
	}
	return nil
}
