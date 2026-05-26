package perf

import (
	"context"
	"time"
)

// TradeRecord is a completed round-trip trade — the analytical primitive for
// every downstream KPI (sharpe, drawdown, win rate, regime attribution, R-multiples).
//
// Decimal-bearing fields are strings to keep the JSON wire format exact
// (no float rounding through the JetStream → PG pipeline). Empty string = NULL.
type TradeRecord struct {
	HelmID string `json:"helm_id"`
	HandID string `json:"hand_id"`
	UserID string `json:"user_id"`

	Symbol    string `json:"symbol"`
	Side      string `json:"side"` // entry side: "buy" (long) or "sell" (short)
	Qty       string `json:"qty,omitzero"`
	Timeframe string `json:"timeframe,omitzero"` // bar timeframe the strategy runs on (M1/M5/H1/…)

	EntryPrice string    `json:"entry_price,omitzero"`
	ExitPrice  string    `json:"exit_price,omitzero"`
	EntryAt    time.Time `json:"entry_at"`
	ExitAt     time.Time `json:"exit_at"`

	GrossPnL   string `json:"gross_pnl,omitzero"`  // (exit-entry)*qty, no fees
	Commission string `json:"commission,omitzero"` // total fees entry+exit; net_pnl computed in PG

	// Risk levels captured at the moment of close.
	StopLossPrice   string `json:"stop_loss_price,omitzero"`
	TakeProfitPrice string `json:"take_profit_price,omitzero"`
	// PlannedRisk is the dollar risk implied at entry: qty × |entry_price − stop_loss|.
	// Empty when no stop loss was set. Used by PG GENERATED `r_multiple = net_pnl / planned_risk`.
	PlannedRisk string `json:"planned_risk,omitzero"`

	// Audit / reconciliation.
	EntryOrderID string `json:"entry_order_id,omitzero"`
	ExitOrderID  string `json:"exit_order_id,omitzero"`
	NEntries     int    `json:"n_entries,omitzero"` // number of entry fills (1 + pyramid adds)

	ExitReason  string `json:"exit_reason,omitzero"`  // signal / sl / tp / kill / bracket_exit / external
	Strategy    string `json:"strategy,omitzero"`     // strategy name
	PatternKind string `json:"pattern_kind,omitzero"` // technical pattern at entry (optional)
	RegimeState string `json:"regime_state,omitzero"` // regime tag at entry (optional)
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
