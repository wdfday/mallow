package signallog

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"mallow/helm/internal/fleet/perf"
	"mallow/helm/internal/infra/natsapi"
)

const signalStream = "HELM_SIGNALS"

// Page is one cursor page of signal results.
type Page struct {
	Signals []natsapi.SignalMsg `json:"signals"`
	Next    time.Time           `json:"next"`
	HasMore bool                `json:"has_more"`
}

// Log queries the PostgreSQL `signals` table (populated by SignalPersister).
type Log struct {
	db *sql.DB
}

func New(db *sql.DB) *Log {
	return &Log{db: db}
}

// Query returns signals for helmID ordered newest-first.
func (l *Log) Query(ctx context.Context, helmID string, page perf.Page) (Page, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = 200
	}

	args := []any{helmID, limit + 1}
	where := "helm_id = $1"
	if !page.After.IsZero() {
		where += " AND generated_at < $3"
		args = append(args, page.After)
	}

	rows, err := l.db.QueryContext(ctx,
		`SELECT id, helm_id, hand_id, user_id, symbol, direction, exit_kind, strength,
			price, target_price, stop_price, is_offset, atr, reason, generated_at, received_at
		 FROM signals
		 WHERE `+where+`
		 ORDER BY generated_at DESC
		 LIMIT $2`,
		args...,
	)
	if err != nil {
		return Page{}, fmt.Errorf("signals query: %w", err)
	}
	defer rows.Close()

	var signals []natsapi.SignalMsg
	for rows.Next() {
		var (
			s                                  natsapi.SignalMsg
			exitKind, reason                   sql.NullString
			price, targetPrice, stopPrice, atr sql.NullString
			generatedAt, receivedAt            sql.NullTime
		)
		if err := rows.Scan(
			&s.ID, &s.HelmID, &s.HandID, &s.UserID, &s.Symbol, &s.Direction, &exitKind, &s.Strength,
			&price, &targetPrice, &stopPrice, &s.IsOffset, &atr, &reason, &generatedAt, &receivedAt,
		); err != nil {
			return Page{}, fmt.Errorf("signals scan: %w", err)
		}
		s.ExitKind = exitKind.String
		s.Reason = reason.String
		s.Price = price.String
		s.TargetPrice = targetPrice.String
		s.StopPrice = stopPrice.String
		s.ATR = atr.String
		s.GeneratedAt = generatedAt.Time
		s.ReceivedAt = receivedAt.Time
		signals = append(signals, s)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("signals rows: %w", err)
	}

	hasMore := len(signals) > limit
	if hasMore {
		signals = signals[:limit]
	}
	var next time.Time
	if hasMore && len(signals) > 0 {
		next = signals[len(signals)-1].GeneratedAt
	}
	return Page{Signals: signals, Next: next, HasMore: hasMore}, nil
}
