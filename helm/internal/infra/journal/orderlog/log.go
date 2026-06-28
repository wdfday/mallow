package orderlog

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/shopspring/decimal"
)

// Log is the read-only view of the `orders` table for handlers / FE queries.
// Writes go through the JetStream helm.pos.> path drained by the Persister.
type Log interface {
	Query(ctx context.Context, f OrderFilter) ([]OrderRecord, error)
	// GetByClientOrderID returns a single order by its mallow clid, or nil if absent.
	// Supports idempotency checks (does this clid already exist?).
	GetByClientOrderID(ctx context.Context, clid string) (*OrderRecord, error)
}

type postgresLog struct {
	db *sql.DB
}

// NewLog returns a read-only Log backed by the orders table.
func NewLog(db *sql.DB) Log {
	return &postgresLog{db: db}
}

const orderCols = `exchange_order_id, client_order_id, helm_id, hand_id, position_id,
	symbol, side, order_type, qty, price, status, filled_qty, filled_price,
	is_close, reason, placed_at, updated_at`

// Query returns orders matching the filter, newest first (by placed_at).
func (l *postgresLog) Query(ctx context.Context, f OrderFilter) ([]OrderRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	args := []any{f.HelmID, limit}
	where := "helm_id = $1"
	idx := 3

	if f.HandID != "" {
		where += fmt.Sprintf(" AND hand_id = $%d", idx)
		args = append(args, f.HandID)
		idx++
	}
	if f.Status != "" {
		where += fmt.Sprintf(" AND status = $%d", idx)
		args = append(args, f.Status)
		idx++
	}
	if !f.After.IsZero() {
		where += fmt.Sprintf(" AND placed_at > $%d", idx)
		args = append(args, f.After)
		idx++
	}
	if !f.Before.IsZero() {
		where += fmt.Sprintf(" AND placed_at < $%d", idx)
		args = append(args, f.Before)
	}

	rows, err := l.db.QueryContext(ctx,
		`SELECT `+orderCols+` FROM orders WHERE `+where+
			` ORDER BY placed_at DESC NULLS LAST LIMIT $2`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("orders query: %w", err)
	}
	defer rows.Close()

	var out []OrderRecord
	for rows.Next() {
		rec, scanErr := scanOrder(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetByClientOrderID returns the order with the given clid, or nil if not found.
func (l *postgresLog) GetByClientOrderID(ctx context.Context, clid string) (*OrderRecord, error) {
	row := l.db.QueryRowContext(ctx,
		`SELECT `+orderCols+` FROM orders WHERE client_order_id = $1`, clid)
	rec, err := scanOrder(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("orders get by clid: %w", err)
	}
	return &rec, nil
}

// scanner abstracts *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(s scanner) (OrderRecord, error) {
	var (
		rec                                        OrderRecord
		clid, handID, posID, side, ordType, reason sql.NullString
		qty, price, filledQty, filledPrice         sql.NullString
		placedAt                                   sql.NullTime
	)
	if err := s.Scan(
		&rec.ExchangeOrderID, &clid, &rec.HelmID, &handID, &posID,
		&rec.Symbol, &side, &ordType, &qty, &price, &rec.Status, &filledQty, &filledPrice,
		&rec.IsClose, &reason, &placedAt, &rec.UpdatedAt,
	); err != nil {
		return rec, err
	}
	rec.ClientOrderID = clid.String
	rec.HandID = handID.String
	rec.PositionID = posID.String
	rec.Side = side.String
	rec.OrderType = ordType.String
	rec.Reason = reason.String
	rec.Qty = parseDec(qty.String)
	rec.Price = parseDec(price.String)
	rec.FilledQty = parseDec(filledQty.String)
	rec.FilledPrice = parseDec(filledPrice.String)
	rec.PlacedAt = placedAt.Time
	return rec, nil
}

func parseDec(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, _ := decimal.NewFromString(s)
	return d
}
