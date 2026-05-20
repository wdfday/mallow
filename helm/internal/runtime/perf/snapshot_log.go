package perf

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// PositionEntry is one open position in a Snapshot.
// For helm-level snapshots, AvgPrice is the portfolio weighted average cost.
// For hand-level snapshots, AvgPrice is the leg's entry price.
type PositionEntry struct {
	Symbol   string          `json:"symbol"`
	Side     string          `json:"side"` // "buy" (long) | "sell" (short)
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
}

// Snapshot is an immutable point-in-time record of portfolio state.
// Recorded after every fill (helm-level) or bar close (hand-level).
//
// HandID="": helm-level snapshot (cash + equity + all positions).
// HandID set: hand-level snapshot (equity + legs owned by this hand; Cash is zero).
//
// Prices in Positions are NOT pre-multiplied — the frontend applies its own
// current prices to compute equity at any timeframe. Equity here is the
// mark-to-market value at snapshot time (cash + sum(qty * current_price)).
type Snapshot struct {
	HelmID    string          `json:"helm_id"`
	HandID    string          `json:"hand_id,omitempty"` // empty = helm-level
	TS        time.Time       `json:"ts"`
	Cash      decimal.Decimal `json:"cash,omitzero"`   // helm-level only (quote free balance)
	Equity    decimal.Decimal `json:"equity,omitzero"` // mark-to-market at TS
	Positions []PositionEntry `json:"positions"`
}

// SnapshotLog is the append-only store for per-helm and per-hand portfolio snapshots.
// Snapshots are recorded after every fill so the log is a step function:
// each entry is valid until the next one for the same entity.
//
// Implementation: JetStream stream HELM_SNAPSHOTS.
// Subjects: helm.snapshot.{helm_id} (helm-level), helm.snapshot.{helm_id}.{hand_id} (hand-level).
// Storage: FileStorage, MaxAge 90 days.
type SnapshotLog interface {
	// Append writes one snapshot. Safe to call from a goroutine; returns quickly.
	Append(ctx context.Context, s Snapshot) error

	// Query returns snapshots for one entity ordered by TS ascending.
	// Pass handID="" for helm-level queries.
	Query(ctx context.Context, helmID, handID string, page Page) (SnapshotPage, error)

	// Latest returns the most recent n snapshots (newest-last).
	// Pass handID="" for helm-level queries.
	Latest(ctx context.Context, helmID, handID string, n int) ([]Snapshot, error)
}

// SnapshotPage is one cursor-paged result from SnapshotLog.Query.
type SnapshotPage struct {
	Snapshots []Snapshot
	Next      time.Time
	HasMore   bool
}
