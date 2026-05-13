package perf

import (
	"context"
	"time"
)

// TradeRecord is a completed round-trip trade persisted to durable storage.
type TradeRecord struct {
	ID      string // uuid assigned on insert
	HandID  string
	Symbol  string
	Side    string // "buy" | "sell"
	Qty     float64
	EntryPx float64
	ExitPx  float64
	EntryTS time.Time
	ExitTS  time.Time
	PnL     float64
	PnLPct  float64
}

// TradeLogPage is one cursor page of results.
type TradeLogPage struct {
	Trades  []TradeRecord
	Next    time.Time // pass as Page.After for the next page; zero = no more data
	HasMore bool
}

// TradeLog is the append-only store for per-hand closed round-trip trades.
//
// Implementation: JetStream stream HELM_TRADES, subjects helm.trades.{hand_id}.
// Storage: FileStorage, MaxAge 365 days, dedup window 30 min
// (Nats-Msg-Id = hand_id+entry_ts_ms+exit_ts_ms).
//
// Dedup contract: Append with the same (hand_id, entry_ts, exit_ts) within the
// dedup window is silently ignored. Safe to call from multiple processes.
type TradeLog interface {
	// Append writes one closed trade. Idempotent on (hand_id, entry_ts, exit_ts).
	Append(ctx context.Context, t TradeRecord) error

	// AppendBatch writes multiple trades in one round-trip. Idempotent.
	AppendBatch(ctx context.Context, trades []TradeRecord) error

	// Query returns cursor-paged trades for one hand ordered by exit_ts ascending.
	// Caller advances the cursor via TradeLogPage.Next.
	Query(ctx context.Context, handID string, page Page) (TradeLogPage, error)

	// Since returns all trades for a hand with exit_ts strictly after the given time.
	// Used by the Reporter and gap recovery to reload trade history after restart.
	Since(ctx context.Context, handID string, after time.Time) ([]TradeRecord, error)
}
