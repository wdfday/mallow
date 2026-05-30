package perf

import (
	"time"

	"github.com/shopspring/decimal"
)

// PositionEntry is one open position in a Snapshot.
type PositionEntry struct {
	Symbol   string          `json:"symbol"`
	Side     string          `json:"side"` // "buy" (long) | "sell" (short)
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
}

// Snapshot is an immutable point-in-time record of helm portfolio state.
// Written to helm.equity.{helm_id} JetStream by SnapshotWorker after fill bursts
// and on a 60s heartbeat. Helm-level only — hand equity is reconstructed from poslog.
//
// Equity = Cash + open-position mark-to-market.
// RealizedPnL = sum of all closed-trade PnL since inception.
// UnrealizedPnL = mark-to-market delta of currently open positions from their entry price.
type Snapshot struct {
	HelmID        string          `json:"helm_id"`
	TS            time.Time       `json:"ts"`
	Cash          decimal.Decimal `json:"cash,omitzero"`
	Equity        decimal.Decimal `json:"equity,omitzero"`
	RealizedPnL   decimal.Decimal `json:"realized_pnl,omitzero"`
	UnrealizedPnL decimal.Decimal `json:"unrealized_pnl,omitzero"`
	Positions     []PositionEntry `json:"positions"`
}
