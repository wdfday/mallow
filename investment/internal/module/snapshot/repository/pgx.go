package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"mallow/investment/internal/module/snapshot/domain"
)

type pgxRepo struct {
	pool *pgxpool.Pool
}

func NewPgx(pool *pgxpool.Pool) Repository {
	return &pgxRepo{pool: pool}
}

func (r *pgxRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.PortfolioSnapshot, error) {
	q := `SELECT
		id, user_id, snapshot_date, snapshot_type,
		total_value::float8, cash_balance::float8, spot_value::float8,
		derivative_value::float8, total_cost::float8,
		unrealized_pnl::float8, realized_pnl::float8,
		total_dividends::float8, total_fees::float8,
		total_return::float8, total_return_pct::float8,
		day_change::float8, day_change_pct::float8,
		cash_inflow::float8, cash_outflow::float8,
		spot_allocation::text, derivative_allocation::text,
		sector_allocation::text, metrics::text,
		source_event_id, created_at, updated_at
	FROM portfolio_snapshots WHERE user_id = $1`

	args := []any{userID}
	idx := 2

	if filter.SnapshotType != "" {
		q += fmt.Sprintf(` AND snapshot_type = $%d`, idx)
		args = append(args, filter.SnapshotType)
		idx++
	}
	q += ` ORDER BY snapshot_date DESC`
	if filter.Limit > 0 {
		q += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, idx, idx+1)
		args = append(args, filter.Limit, filter.Offset)
	}

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []domain.PortfolioSnapshot
	for rows.Next() {
		var s domain.PortfolioSnapshot
		err := rows.Scan(
			&s.ID, &s.UserID, &s.SnapshotDate, &s.SnapshotType,
			&s.TotalValue, &s.CashBalance, &s.SpotValue,
			&s.DerivativeValue, &s.TotalCost,
			&s.UnrealizedPnL, &s.RealizedPnL,
			&s.TotalDividends, &s.TotalFees,
			&s.TotalReturn, &s.TotalReturnPct,
			&s.DayChange, &s.DayChangePct,
			&s.CashInflow, &s.CashOutflow,
			&s.SpotAllocation, &s.DerivativeAlloc,
			&s.SectorAllocation, &s.Metrics,
			&s.SourceEventID, &s.CreatedAt, &s.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		snaps = append(snaps, s)
	}
	return snaps, rows.Err()
}
