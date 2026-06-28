// Package orderlog projects the order lifecycle events from the HELM_POSITIONS
// JetStream (helm.pos.>) into a queryable `orders` table in PostgreSQL.
//
// poslog (helm.pos.>) is the durable write-ahead log replayed in-memory by the
// reconciler on restart; it is not queryable. This package adds the missing
// read model: a single durable consumer drains order_placed / order_filled /
// order_cancelled events and upserts one row per order.
//
// Keying: the table is keyed by exchange_order_id (present on every poslog order
// event). The mallow-generated client_order_id (clid) is carried as an indexed
// column for correlation and idempotency lookups. See CLIENT_ORDER_ID.md.
package orderlog

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Migrate creates the orders table and its indexes (idempotent).
func Migrate(db *gorm.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS orders (
			exchange_order_id TEXT        PRIMARY KEY,
			client_order_id   TEXT,
			helm_id           UUID        NOT NULL,
			hand_id           UUID,
			position_id       TEXT,
			symbol            TEXT        NOT NULL,
			side              TEXT,
			order_type        TEXT,
			qty               NUMERIC(36,18),
			price             NUMERIC(36,18),
			status            TEXT        NOT NULL,
			filled_qty        NUMERIC(36,18),
			filled_price      NUMERIC(36,18),
			is_close          BOOLEAN     NOT NULL DEFAULT FALSE,
			reason            TEXT,
			placed_at         TIMESTAMPTZ,
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_orders_clid ON orders (client_order_id) WHERE client_order_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_orders_hand ON orders (hand_id, placed_at DESC) WHERE hand_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS idx_orders_helm ON orders (helm_id, placed_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_orders_status ON orders (status)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("orders migrate: %w", err)
			}
		}
	}
	return nil
}
