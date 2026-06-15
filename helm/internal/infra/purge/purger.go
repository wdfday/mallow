package purge

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// HelmDataPurger deletes all PostgreSQL audit rows scoped to a helm/account.
// Each table deletion is best-effort — a failure is logged but does not abort the others.
type HelmDataPurger struct {
	db *gorm.DB
}

func New(db *gorm.DB) *HelmDataPurger {
	return &HelmDataPurger{db: db}
}

// PurgeByHelm deletes rows from fills, trades, orders, equity_snapshots, and helm_events
// for the given helm and account IDs.
func (p *HelmDataPurger) PurgeByHelm(helmID, accountID uuid.UUID) error {
	hid := helmID.String()
	aid := accountID.String()

	type purgeTarget struct {
		table string
		where string
		args  []any
	}
	targets := []purgeTarget{
		{"fills", "helm_id = ?", []any{hid}},
		{"trades", "helm_id = ?", []any{hid}},
		{"orders", "helm_id = ?", []any{hid}},
		{"equity_snapshots", "helm_id = ?", []any{hid}},
		{"helm_events", "helm_id = ?", []any{hid}},
	}
	// fills also carries account_id — covered by helm_id above, but log it for clarity.
	_ = aid

	var errs []error
	for _, t := range targets {
		if err := p.db.Exec(fmt.Sprintf("DELETE FROM %s WHERE %s", t.table, t.where), t.args...).Error; err != nil {
			slog.Warn("purge: failed to delete rows (non-fatal)", "table", t.table, "helm_id", hid, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", t.table, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("purge incomplete (%d table(s) failed): %v", len(errs), errs[0])
	}
	slog.Info("purge: helm PG audit data deleted", "helm_id", hid)
	return nil
}
