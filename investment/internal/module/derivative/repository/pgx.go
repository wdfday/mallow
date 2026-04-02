package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"mallow/investment/internal/module/derivative/domain"
)

type pgxRepo struct {
	pool *pgxpool.Pool
}

func NewPgx(pool *pgxpool.Pool) Repository {
	return &pgxRepo{pool: pool}
}

func (r *pgxRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.DerivativePosition, error) {
	q := `SELECT
		id, user_id, symbol, underlying, instrument_type, side, currency,
		quantity::float8, entry_price::float8, current_price::float8,
		leverage::float8, margin_used::float8, liquidation_price::float8,
		contract_size::float8, strike_price::float8, option_type, expiry_date,
		unrealized_pnl::float8, realized_pnl::float8,
		status, opened_at, closed_at, open_event_id, close_event_id, updated_at
	FROM derivative_positions WHERE user_id = $1`

	args := []any{userID}
	if filter.Status != "" {
		q += ` AND status = $2`
		args = append(args, filter.Status)
	}
	q += ` ORDER BY opened_at DESC`

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var positions []domain.DerivativePosition
	for rows.Next() {
		p, err := scanDerivative(rows)
		if err != nil {
			return nil, err
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

func scanDerivative(rows pgx.Rows) (domain.DerivativePosition, error) {
	var p domain.DerivativePosition
	var closedAt *time.Time
	var liqPrice, strikePrice *float64
	var optionType, expiryDate *string
	var closeEventIDRaw pgtype.UUID

	err := rows.Scan(
		&p.ID, &p.UserID, &p.Symbol, &p.Underlying, &p.InstrumentType, &p.Side, &p.Currency,
		&p.Quantity, &p.EntryPrice, &p.CurrentPrice,
		&p.Leverage, &p.MarginUsed, &liqPrice,
		&p.ContractSize, &strikePrice, &optionType, &expiryDate,
		&p.UnrealizedPnL, &p.RealizedPnL,
		&p.Status, &p.OpenedAt, &closedAt, &p.OpenEventID, &closeEventIDRaw, &p.UpdatedAt,
	)
	if err != nil {
		return p, err
	}

	p.LiquidationPrice = liqPrice
	p.StrikePrice = strikePrice
	p.OptionType = optionTypeStr(optionType)
	p.ExpiryDate = expiryDate
	p.ClosedAt = closedAt
	if closeEventIDRaw.Valid {
		id := uuid.UUID(closeEventIDRaw.Bytes)
		p.CloseEventID = &id
	}
	return p, nil
}

func optionTypeStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
