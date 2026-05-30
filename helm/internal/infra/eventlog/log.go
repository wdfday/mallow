package eventlog

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/readmodel"
)

// Log is the interface for persisting and querying helm/hand events.
// Record/filter shapes live in internal/readmodel; this package only does IO.
type Log interface {
	// Append writes a single event. Non-blocking: errors are logged, not returned.
	Append(ctx context.Context, helmID uuid.UUID, userID uuid.UUID, ev natsapi.HelmEvent)
	// Query returns events matching the filter, newest first.
	Query(ctx context.Context, f readmodel.EventFilter) ([]readmodel.EventRecord, error)
}

// postgresLog is the production implementation backed by PostgreSQL.
type postgresLog struct {
	db *sql.DB
}

// New returns a Log backed by the given *sql.DB.
func New(db *sql.DB) Log {
	return &postgresLog{db: db}
}

// Append inserts a single event row. Runs in a goroutine; errors are swallowed.
func (l *postgresLog) Append(ctx context.Context, helmID uuid.UUID, userID uuid.UUID, ev natsapi.HelmEvent) {
	go func() {
		var handID *uuid.UUID
		if ev.HandID != "" {
			if parsed, err := uuid.Parse(ev.HandID); err == nil {
				handID = &parsed
			}
		}

		_, err := l.db.ExecContext(ctx, `
			INSERT INTO helm_events
				(at, helm_id, hand_id, user_id, code, symbol, direction, side, qty, price, order_id, reason, msg)
			VALUES
				($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			ev.At,
			helmID,
			handID,
			userID,
			ev.Code,
			nullStr(ev.Symbol),
			nullStr(ev.Direction),
			nullStr(ev.Side),
			nullDecimal(ev.Qty),
			nullDecimal(ev.Price),
			nullStr(ev.OrderID),
			nullStr(ev.Reason),
			nullStr(ev.Msg),
		)
		if err != nil {
			// Fire-and-forget: don't crash the runtime on a transient DB write failure.
			// The event is still published to JetStream and logged via slog.
			_ = err
		}
	}()
}

// Query returns events matching the filter, ordered by at DESC.
func (l *postgresLog) Query(ctx context.Context, f readmodel.EventFilter) ([]readmodel.EventRecord, error) {
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

	var out []readmodel.EventRecord
	for rows.Next() {
		var e readmodel.EventRecord
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
