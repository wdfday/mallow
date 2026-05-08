package perf

import (
	"context"
	"time"
)

// EquityPoint is one mark-to-market snapshot written after each bar close.
type EquityLogPoint struct {
	HandID string
	TS     time.Time // bar close time (dedup key with HandID)
	Equity float64   // total equity in quote currency
}

// Page is a cursor-based page request.
// After=zero value means from the beginning.
// Limit=0 defaults to a reasonable page size (implementation-defined).
type Page struct {
	After time.Time // exclusive lower bound on TS; zero = from start
	Limit int
}

// EquityLogPage is one page of results with a cursor for the next page.
type EquityLogPage struct {
	Points  []EquityLogPoint
	Next    time.Time // pass as Page.After to fetch the next page; zero = no more data
	HasMore bool
}

// EquityLog is the append-only store for per-hand equity curve points.
//
// Schema (Postgres):
//
//	CREATE TABLE hand_equity_log (
//	    hand_id  UUID    NOT NULL,
//	    ts       BIGINT  NOT NULL,   -- Unix ms; bar close time
//	    equity   FLOAT8  NOT NULL,
//	    PRIMARY KEY (hand_id, ts)    -- dedup: same bar emitted twice → no-op
//	);
//	CREATE INDEX IF NOT EXISTS idx_hand_equity_log_hand_ts ON hand_equity_log(hand_id, ts);
//
// Migration for existing tables:
//
//	CREATE TABLE IF NOT EXISTS hand_equity_log ( ... );
//
// Dedup contract: Append with a (hand_id, ts) that already exists is silently
// ignored (ON CONFLICT DO NOTHING). Safe to call on every bar from multiple
// processes.
type EquityLog interface {
	// Append writes one equity snapshot. Idempotent on (hand_id, ts).
	Append(ctx context.Context, p EquityLogPoint) error

	// AppendBatch writes multiple snapshots in one round-trip. Idempotent.
	AppendBatch(ctx context.Context, pts []EquityLogPoint) error

	// Query returns a cursor-paged slice of equity points for one hand,
	// ordered by ts ascending. Caller advances the cursor via EquityLogPage.Next.
	Query(ctx context.Context, handID string, page Page) (EquityLogPage, error)

	// Latest returns the most recent N points for a hand (newest-last).
	// Useful for dashboard "last 24h" views without full pagination.
	Latest(ctx context.Context, handID string, n int) ([]EquityLogPoint, error)
}
