package eventlog

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Log is the read interface over the helm_events table. The sole writer is the
// Persister (helm.events.> → postgres); this interface only queries.
type Log interface {
	// Query returns events matching the filter, newest first.
	Query(ctx context.Context, f EventFilter) ([]EventRecord, error)
	// CountHandEvents returns event counts grouped by code for a single hand.
	// Used to rebuild a hand's activity counters on restart.
	CountHandEvents(ctx context.Context, handID uuid.UUID) (map[int]int64, error)
}

// postgresLog is the production implementation backed by PostgreSQL.
type postgresLog struct {
	db *sql.DB
}

// New returns a Log backed by the given *sql.DB.
func New(db *sql.DB) Log {
	return &postgresLog{db: db}
}

// Query returns events matching the filter, ordered by at DESC.
func (l *postgresLog) Query(ctx context.Context, f EventFilter) ([]EventRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	args := []any{f.HelmID, limit}
	where := "helm_id = $1"
	idx := 3

	if f.HandID != nil {
		where += " AND hand_id = $" + fmt.Sprint(idx)
		args = append(args, *f.HandID)
		idx++
	}
	if !f.After.IsZero() {
		where += " AND at > $" + fmt.Sprint(idx)
		args = append(args, f.After)
		idx++
	}
	if !f.Before.IsZero() {
		where += " AND at < $" + fmt.Sprint(idx)
		args = append(args, f.Before)
	}

	rows, err := l.db.QueryContext(ctx,
		"SELECT id, at, helm_id, hand_id, user_id, code, symbol, direction, side, qty, price, order_id, reason, msg "+
			"FROM helm_events WHERE "+where+" ORDER BY at DESC LIMIT $2",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var handID sql.NullString
		var symbol, direction, side, orderID, reason, msg sql.NullString
		var qty, price sql.NullString

		if err := rows.Scan(
			&e.ID, &e.At, &e.HelmID, &handID, &e.UserID,
			&e.Code,
			&symbol, &direction, &side, &qty, &price,
			&orderID, &reason, &msg,
		); err != nil {
			return nil, err
		}
		if handID.Valid {
			if parsed, err := uuid.Parse(handID.String); err == nil {
				e.HandID = &parsed
			}
		}
		e.Symbol = symbol.String
		e.Direction = direction.String
		e.Side = side.String
		e.OrderID = orderID.String
		e.Reason = reason.String
		e.Msg = msg.String
		if qty.Valid {
			e.Qty, _ = decimal.NewFromString(qty.String)
		}
		if price.Valid {
			e.Price, _ = decimal.NewFromString(price.String)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountHandEvents returns event counts grouped by code for one hand, in a single
// aggregate query (SELECT code, COUNT(*) ... GROUP BY code).
func (l *postgresLog) CountHandEvents(ctx context.Context, handID uuid.UUID) (map[int]int64, error) {
	rows, err := l.db.QueryContext(ctx,
		"SELECT code, COUNT(*) FROM helm_events WHERE hand_id = $1 GROUP BY code",
		handID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int]int64)
	for rows.Next() {
		var code int
		var n int64
		if err := rows.Scan(&code, &n); err != nil {
			return nil, err
		}
		out[code] = n
	}
	return out, rows.Err()
}

// ── helpers ──────────────────────────────────────────────────────────────────

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullDecimal(d decimal.Decimal) sql.NullString {
	if d.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: d.String(), Valid: true}
}
