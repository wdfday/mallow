package repository

import (
	"context"
	"errors"
	"time"

	"mallow/investment/internal/module/position/domain"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgxRepo struct {
	pool *pgxpool.Pool
}

func NewPgx(pool *pgxpool.Pool) Repository {
	return &pgxRepo{pool: pool}
}

const selectPositionCols = `
	id, account_id, user_id, symbol, name, asset_type, exchange, currency,
	quantity::float8, avg_cost::float8, total_cost::float8,
	current_price::float8, current_value::float8,
	unrealized_pnl::float8, unrealized_pct::float8,
	realized_pnl::float8, total_dividends::float8, portfolio_weight::float8,
	status, last_seq, opened_at, closed_at, updated_at
FROM positions`

func (r *pgxRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.PortfolioPosition, error) {
	query := `SELECT ` + selectPositionCols + ` WHERE user_id = $1`
	args := []any{userID}

	if filter.Status != "" {
		query += ` AND status = $2`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY portfolio_weight DESC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var positions []domain.PortfolioPosition
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			return nil, err
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

func (r *pgxRepo) ListByAccountID(ctx context.Context, accountID uuid.UUID, filter ListFilter) ([]domain.PortfolioPosition, error) {
	query := `SELECT ` + selectPositionCols + ` WHERE account_id = $1`
	args := []any{accountID}

	if filter.Status != "" {
		query += ` AND status = $2`
		args = append(args, filter.Status)
	}
	query += ` ORDER BY portfolio_weight DESC`

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var positions []domain.PortfolioPosition
	for rows.Next() {
		p, err := scanPosition(rows)
		if err != nil {
			return nil, err
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

func (r *pgxRepo) GetBySymbol(ctx context.Context, userID uuid.UUID, symbol string) (*domain.PortfolioPosition, error) {
	query := `SELECT ` + selectPositionCols + ` WHERE user_id = $1 AND symbol = $2`
	rows, err := r.pool.Query(ctx, query, userID, symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("position not found")
	}
	p, err := scanPosition(rows)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func scanPosition(rows pgx.Rows) (domain.PortfolioPosition, error) {
	var p domain.PortfolioPosition
	var closedAt *time.Time
	err := rows.Scan(
		&p.ID, &p.AccountID, &p.UserID, &p.Symbol, &p.Name, &p.AssetType, &p.Exchange, &p.Currency,
		&p.Quantity, &p.AvgCost, &p.TotalCost,
		&p.CurrentPrice, &p.CurrentValue,
		&p.UnrealizedPnL, &p.UnrealizedPct,
		&p.RealizedPnL, &p.TotalDividends, &p.PortfolioWeight,
		&p.Status, &p.LastSeq, &p.OpenedAt, &closedAt, &p.UpdatedAt,
	)
	if err != nil {
		return p, err
	}
	p.ClosedAt = closedAt
	return p, nil
}
