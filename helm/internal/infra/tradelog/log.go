package tradelog

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// TradeRecord is a completed round-trip trade (entry → exit).
// Mirrors all analytical columns of the `trades` table, including the
// PG GENERATED ones (net_pnl, holding_seconds, r_multiple).
type TradeRecord struct {
	ID         uuid.UUID
	HandID     uuid.UUID
	HelmID     uuid.UUID
	UserID     uuid.UUID
	Symbol     string
	Side       string
	Timeframe  string
	EntryPrice decimal.Decimal
	ExitPrice  decimal.Decimal
	Qty        decimal.Decimal
	PnL        decimal.Decimal // gross PnL = (exit-entry)*qty
	Commission decimal.Decimal // total entry+exit fees
	NetPnL     decimal.Decimal // PG GENERATED = PnL − Commission

	StopLossPrice   decimal.Decimal
	TakeProfitPrice decimal.Decimal
	PlannedRisk     decimal.Decimal // qty × |entry − SL|
	RMultiple       decimal.Decimal // PG GENERATED = net_pnl / planned_risk

	EntryOrderID   string
	ExitOrderID    string
	NEntries       int
	HoldingSeconds int // PG GENERATED

	Source      string // exit reason: signal / sl / tp / kill / bracket_exit / external / release
	Strategy    string
	PatternKind string
	RegimeState string

	EntryAt time.Time
	ExitAt  time.Time
}

// Filter constrains a Query call.
type Filter struct {
	HandID *uuid.UUID
	HelmID *uuid.UUID
	UserID uuid.UUID
	After  time.Time
	Before time.Time
	Limit  int
}

// Log is the read-only view of the trades table for handlers/FE queries.
//
// Writes go through the JetStream HELM_TRADES path: hand → perf.TradeLog →
// TradePersister → PostgreSQL `trades`. Direct PG inserts from this package
// are deliberately removed so the durable buffer absorbs PG outages.
type Log interface {
	Query(ctx context.Context, f Filter) ([]TradeRecord, error)
}

type postgresLog struct {
	db *sql.DB
}

func New(db *sql.DB) Log {
	return &postgresLog{db: db}
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
		`SELECT id, hand_id, helm_id, user_id, symbol, side, timeframe,
			entry_price, exit_price, qty, pnl, commission, net_pnl,
			stop_loss_price, take_profit_price, planned_risk, r_multiple,
			entry_order_id, exit_order_id, n_entries, holding_seconds,
			source, strategy, pattern_kind, regime_state,
			entry_at, exit_at
		FROM trades WHERE `+where+` ORDER BY exit_at DESC LIMIT $2`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []TradeRecord
	for rows.Next() {
		var r TradeRecord
		var (
			timeframe                                  sql.NullString
			entryPrice, exitPrice, qty                 sql.NullString
			pnl, commission, netPnL                    sql.NullString
			slPrice, tpPrice, plannedRisk, rMultiple   sql.NullString
			entryOrderID, exitOrderID                  sql.NullString
			nEntries, holdingSeconds                   sql.NullInt32
			source, strategy, patternKind, regimeState sql.NullString
			entryAt                                    sql.NullTime
		)

		if err := rows.Scan(
			&r.ID, &r.HandID, &r.HelmID, &r.UserID, &r.Symbol, &r.Side, &timeframe,
			&entryPrice, &exitPrice, &qty, &pnl, &commission, &netPnL,
			&slPrice, &tpPrice, &plannedRisk, &rMultiple,
			&entryOrderID, &exitOrderID, &nEntries, &holdingSeconds,
			&source, &strategy, &patternKind, &regimeState,
			&entryAt, &r.ExitAt,
		); err != nil {
			return nil, err
		}
		r.Timeframe = timeframe.String
		r.EntryPrice = decimalOrZero(entryPrice)
		r.ExitPrice = decimalOrZero(exitPrice)
		r.Qty = decimalOrZero(qty)
		r.PnL = decimalOrZero(pnl)
		r.Commission = decimalOrZero(commission)
		r.NetPnL = decimalOrZero(netPnL)
		r.StopLossPrice = decimalOrZero(slPrice)
		r.TakeProfitPrice = decimalOrZero(tpPrice)
		r.PlannedRisk = decimalOrZero(plannedRisk)
		r.RMultiple = decimalOrZero(rMultiple)
		r.EntryOrderID = entryOrderID.String
		r.ExitOrderID = exitOrderID.String
		if nEntries.Valid {
			r.NEntries = int(nEntries.Int32)
		}
		if holdingSeconds.Valid {
			r.HoldingSeconds = int(holdingSeconds.Int32)
		}
		r.Source = source.String
		r.Strategy = strategy.String
		r.PatternKind = patternKind.String
		r.RegimeState = regimeState.String
		if entryAt.Valid {
			r.EntryAt = entryAt.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func decimalOrZero(s sql.NullString) decimal.Decimal {
	if !s.Valid {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s.String)
	if err != nil {
		return decimal.Zero
	}
	return d
}
