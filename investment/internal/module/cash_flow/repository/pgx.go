package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"mallow/investment/internal/module/cash_flow/domain"
)

type pgxRepo struct {
	pool *pgxpool.Pool
}

func NewPgx(pool *pgxpool.Pool) Repository {
	return &pgxRepo{pool: pool}
}

func (r *pgxRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.PortfolioCashFlow, error) {
	q := `SELECT id, user_id, flow_type, amount::float8, currency,
		symbol, description, source_event_id, occurred_at, created_at
	FROM portfolio_cash_flows WHERE user_id = $1`

	args := []any{userID}
	idx := 2

	if filter.FlowType != "" {
		q += fmt.Sprintf(` AND flow_type = $%d`, idx)
		args = append(args, filter.FlowType)
		idx++
	}
	q += ` ORDER BY occurred_at DESC`
	if filter.Limit > 0 {
		q += fmt.Sprintf(` LIMIT $%d OFFSET $%d`, idx, idx+1)
		args = append(args, filter.Limit, filter.Offset)
	}

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var flows []domain.PortfolioCashFlow
	for rows.Next() {
		var f domain.PortfolioCashFlow
		var symbol, description *string
		err := rows.Scan(
			&f.ID, &f.UserID, &f.FlowType, &f.Amount, &f.Currency,
			&symbol, &description, &f.SourceEventID, &f.OccurredAt, &f.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		if symbol != nil {
			f.Symbol = *symbol
		}
		if description != nil {
			f.Description = *description
		}
		flows = append(flows, f)
	}
	return flows, rows.Err()
}
