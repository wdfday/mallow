package perf

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// PositionEntry is one open position in a PortfolioSnapshot.
// For helm-level snapshots, AvgPrice is the portfolio weighted average cost.
// For hand-level snapshots, AvgPrice is the leg's entry price.
type PositionEntry struct {
	Symbol   string          `json:"symbol"`
	Side     string          `json:"side"` // "buy" (long) | "sell" (short)
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
}

// PortfolioSnapshot is an immutable point-in-time record of portfolio state,
// recorded after every fill. Prices are NOT pre-multiplied — the frontend
// applies its own current prices to compute equity at any timeframe.
//
// HandID="": helm-level snapshot (cash + all positions across all hands of this helm).
// HandID set: hand-level snapshot (legs owned by this hand; Cash is always zero).
type PortfolioSnapshot struct {
	HelmID    string          `json:"helm_id"`
	HandID    string          `json:"hand_id,omitempty"` // empty = helm-level
	TS        time.Time       `json:"ts"`
	Cash      decimal.Decimal `json:"cash,omitempty"` // helm-level only
	Positions []PositionEntry `json:"positions"`
}

// PortfolioLog is the append-only store for per-helm and per-hand portfolio snapshots.
// Snapshots are recorded after every fill (not on a timer) so the log is a step function:
// each entry is valid until the next one for the same entity.
//
// Implementation: JetStream stream PORTFOLIO_SNAPSHOTS.
// Subjects: portfolio.{helm_id} (helm-level), portfolio.{helm_id}.{hand_id} (hand-level).
// Storage: FileStorage, MaxAge 90 days.
type PortfolioLog interface {
	// Append writes one snapshot. Safe to call from a goroutine; returns quickly.
	Append(ctx context.Context, s PortfolioSnapshot) error

	// Query returns snapshots for one entity ordered by TS ascending.
	// Pass handID="" for helm-level queries.
	Query(ctx context.Context, helmID, handID string, page Page) (PortfolioPage, error)

	// Latest returns the most recent n snapshots (newest-last).
	// Pass handID="" for helm-level queries.
	Latest(ctx context.Context, helmID, handID string, n int) ([]PortfolioSnapshot, error)
}

// PortfolioPage is one cursor-paged result from PortfolioLog.Query.
type PortfolioPage struct {
	Snapshots []PortfolioSnapshot
	Next      time.Time
	HasMore   bool
}
