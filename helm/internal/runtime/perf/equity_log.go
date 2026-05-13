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
// Implementation: JetStream stream HELM_EQUITY, subjects helm.equity.{hand_id}.
// Storage: FileStorage, MaxAge 90 days, dedup window 5 min (Nats-Msg-Id = hand_id+ts_ms).
//
// Dedup contract: Append with the same (hand_id, ts) within the dedup window is
// silently ignored. Safe to call on every bar or on restart.
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
