package tradelog

import (
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
	RegimeState string

	EntryAt time.Time
	ExitAt  time.Time
}

// TradeFilter constrains a trades Query call.
type TradeFilter struct {
	HandID *uuid.UUID
	HelmID *uuid.UUID
	UserID uuid.UUID
	After  time.Time
	Before time.Time
	Limit  int
}
