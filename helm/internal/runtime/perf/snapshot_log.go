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
// Recorded after every fill.
//
// HandID="": helm-level snapshot (full account view).
// HandID set: hand-level snapshot (legs owned by this hand only).
//
// Equity = Cash + open-position mark-to-market.
// RealizedPnL = sum of all closed-trade PnL since inception.
// UnrealizedPnL = mark-to-market delta of currently open positions from their entry price.
// All four numeric fields are always populated. For shared-pool hands (no AllocatedCapital),
// Equity = realizedPnL + unrealizedPnL (net contribution); Cash = realizedPnL (liquid portion).
type Snapshot struct {
	HelmID        string          `json:"helm_id"`
	HandID        string          `json:"hand_id,omitempty"` // empty = helm-level
	TS            time.Time       `json:"ts"`
	Cash          decimal.Decimal `json:"cash,omitzero"`           // free quote balance (helm) or liquid within budget (hand)
	Equity        decimal.Decimal `json:"equity,omitzero"`         // Cash + open-position MtM; zero for shared-pool hands
	RealizedPnL   decimal.Decimal `json:"realized_pnl,omitzero"`   // sum of closed-trade PnL
	UnrealizedPnL decimal.Decimal `json:"unrealized_pnl,omitzero"` // open-position MtM delta from entry
	Positions     []PositionEntry `json:"positions"`
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
