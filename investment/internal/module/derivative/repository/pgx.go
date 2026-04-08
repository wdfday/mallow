package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
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
		quantity::text, entry_price::text, current_price::text,
		leverage::float8, margin_used::text, liquidation_price::text,
		contract_size::text, strike_price::text, option_type, expiry_date,
		unrealized_pnl::text, realized_pnl::text,
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
	var liqPriceStr, strikePriceStr *string
	var optionType, expiryDate *string
	var closeEventIDRaw pgtype.UUID
	var qtyStr, entryStr, curStr, marginStr, contractStr, uplStr, realStr string

	err := rows.Scan(
		&p.ID, &p.UserID, &p.Symbol, &p.Underlying, &p.InstrumentType, &p.Side, &p.Currency,
		&qtyStr, &entryStr, &curStr,
		&p.Leverage, &marginStr, &liqPriceStr,
		&contractStr, &strikePriceStr, &optionType, &expiryDate,
		&uplStr, &realStr,
		&p.Status, &p.OpenedAt, &closedAt, &p.OpenEventID, &closeEventIDRaw, &p.UpdatedAt,
	)
	if err != nil {
		return p, err
	}

	p.Quantity, _ = decimal.NewFromString(qtyStr)
	p.EntryPrice, _ = decimal.NewFromString(entryStr)
	p.CurrentPrice, _ = decimal.NewFromString(curStr)
	p.MarginUsed, _ = decimal.NewFromString(marginStr)
	p.ContractSize, _ = decimal.NewFromString(contractStr)
	p.UnrealizedPnL, _ = decimal.NewFromString(uplStr)
	p.RealizedPnL, _ = decimal.NewFromString(realStr)

	if liqPriceStr != nil {
		d, _ := decimal.NewFromString(*liqPriceStr)
		p.LiquidationPrice = &d
	}
	if strikePriceStr != nil {
		d, _ := decimal.NewFromString(*strikePriceStr)
		p.StrikePrice = &d
	}
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
