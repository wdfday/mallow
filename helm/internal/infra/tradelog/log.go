package tradelog

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TradeRecord is a completed round-trip trade (entry → exit).
type TradeRecord struct {
	ID         uuid.UUID
	HandID     uuid.UUID
	HelmID     uuid.UUID
	UserID     uuid.UUID
	Symbol     string
	Side       string // entry side: "buy" (long) or "sell" (short)
	EntryPrice decimal.Decimal
	ExitPrice  decimal.Decimal
	Qty        decimal.Decimal
	PnL        decimal.Decimal
	Commission decimal.Decimal
	Source     string // signal / sl / tp / kill / bracket_exit
	Strategy   string
	EntryAt    time.Time
	ExitAt     time.Time
}

// Filter constrains a Query call.
type Filter struct {
	HandID *uuid.UUID // nil = all hands for the helm
	HelmID *uuid.UUID // nil = all helms for the user
	UserID uuid.UUID  // required
	After  time.Time  // zero = no lower bound
	Before time.Time  // zero = no upper bound
	Limit  int        // 0 = default (100)
}

// Log persists and queries completed trades.
type Log interface {
	// Append writes a trade record. Fire-and-forget: errors are logged, not returned.
	Append(ctx context.Context, rec TradeRecord)
	// Query returns trades matching the filter, newest first.
	Query(ctx context.Context, f Filter) ([]TradeRecord, error)
}

// postgresLog is the production implementation backed by PostgreSQL.
type postgresLog struct {
	db *sql.DB
}

// New returns a Log backed by the given *sql.DB.
func New(db *sql.DB) Log {
	return &postgresLog{db: db}
}

// Append inserts a single trade row. Runs in a goroutine to stay non-blocking.
// On dedup conflict (same hand_id + entry_at + exit_at) the row is silently skipped.
func (l *postgresLog) Append(ctx context.Context, rec TradeRecord) {
	go func() {
		_, err := l.db.ExecContext(ctx, `
			INSERT INTO trades
				(hand_id, helm_id, user_id, symbol, side,
				 entry_price, exit_price, qty, pnl, commission,
				 source, strategy, entry_at, exit_at)
			VALUES
				($1, $2, $3, $4, $5,
				 $6, $7, $8, $9, $10,
				 $11, $12, $13, $14)
			ON CONFLICT (hand_id, entry_at, exit_at) DO NOTHING`,
			rec.HandID, rec.HelmID, rec.UserID, rec.Symbol, rec.Side,
			nullDecimal(rec.EntryPrice), nullDecimal(rec.ExitPrice),
			nullDecimal(rec.Qty), nullDecimal(rec.PnL), rec.Commission.String(),
			nullStr(rec.Source), nullStr(rec.Strategy),
			nullTime(rec.EntryAt), rec.ExitAt,
		)
		if err != nil {
			slog.Error("tradelog: append failed", "hand_id", rec.HandID, "err", err)
		}
	}()
}

// Query returns trades matching the filter, ordered by exit_at DESC.
func (l *postgresLog) Query(ctx context.Context, f Filter) ([]TradeRecord, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	args := []any{f.UserID, limit}
	where := "user_id = $1"
	idx := 3

	if f.HelmID != nil {
		where += fmt.Sprintf(" AND helm_id = $%d", idx)
		args = append(args, *f.HelmID)
		idx++
	}
	if f.HandID != nil {
		where += fmt.Sprintf(" AND hand_id = $%d", idx)
		args = append(args, *f.HandID)
		idx++
	}
	if !f.After.IsZero() {
		where += fmt.Sprintf(" AND exit_at > $%d", idx)
		args = append(args, f.After)
		idx++
	}
	if !f.Before.IsZero() {
		where += fmt.Sprintf(" AND exit_at < $%d", idx)
		args = append(args, f.Before)
	}

	rows, err := l.db.QueryContext(ctx,
		"SELECT id, hand_id, helm_id, user_id, symbol, side,"+
			" entry_price, exit_price, qty, pnl, commission,"+
			" source, strategy, entry_at, exit_at, created_at"+
			" FROM trades WHERE "+where+" ORDER BY exit_at DESC LIMIT $2",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TradeRecord
	for rows.Next() {
		var r TradeRecord
		var entryPrice, exitPrice, qty, pnl, commission sql.NullString
		var source, strategy sql.NullString
		var entryAt sql.NullTime
		var createdAt time.Time

		if err := rows.Scan(
			&r.ID, &r.HandID, &r.HelmID, &r.UserID, &r.Symbol, &r.Side,
			&entryPrice, &exitPrice, &qty, &pnl, &commission,
			&source, &strategy, &entryAt, &r.ExitAt, &createdAt,
		); err != nil {
			return nil, err
		}
		if entryPrice.Valid {
			r.EntryPrice, _ = decimal.NewFromString(entryPrice.String)
		}
		if exitPrice.Valid {
			r.ExitPrice, _ = decimal.NewFromString(exitPrice.String)
		}
		if qty.Valid {
			r.Qty, _ = decimal.NewFromString(qty.String)
		}
		if pnl.Valid {
			r.PnL, _ = decimal.NewFromString(pnl.String)
		}
		if commission.Valid {
			r.Commission, _ = decimal.NewFromString(commission.String)
		}
		r.Source = source.String
		r.Strategy = strategy.String
		if entryAt.Valid {
			r.EntryAt = entryAt.Time
		}
		out = append(out, r)
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

func nullTime(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: !t.IsZero()}
}
