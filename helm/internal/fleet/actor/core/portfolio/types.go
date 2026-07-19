package portfolio

import (
	"time"

	"github.com/shopspring/decimal"
)

// Side represents the direction of a trade.
type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

// Position represents an open position for one symbol.
// Qty is positive for a long position, negative for a short position.
//
// UnrealizedPnL/MarketValue are locally computed from AvgPrice/CurrentPrice
// (see update.go) — NOT the exchange-reported figure. The margin/futures
// fields below (Side, Leverage, LiquidationPrice, MarginMode) are populated
// from SyncedPosition on the next REST sync (see ApplySync); zero-valued for
// spot positions and until the first sync after a fresh entry.
type Position struct {
	Symbol          string          `json:"symbol"`
	Qty             decimal.Decimal `json:"qty"`
	AvgPrice        decimal.Decimal `json:"avg_price"`
	CurrentPrice    decimal.Decimal `json:"current_price"`
	UnrealizedPnL   decimal.Decimal `json:"unrealized_pnl"`
	MarketValue     decimal.Decimal `json:"market_value"`
	EntryTimestamp  time.Time       `json:"entry_timestamp"`
	EntryCommission decimal.Decimal `json:"entry_commission"`

	Side             string          `json:"side,omitempty"`
	Leverage         decimal.Decimal `json:"leverage"`
	LiquidationPrice decimal.Decimal `json:"liquidation_price"`
	MarginMode       string          `json:"margin_mode,omitempty"`
}

// Fill represents a confirmed order execution.
type Fill struct {
	Timestamp  time.Time       `json:"timestamp"`
	HandID     string          `json:"hand_id"`
	Symbol     string          `json:"symbol"`
	Side       Side            `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	Price      decimal.Decimal `json:"price"`
	Commission decimal.Decimal `json:"commission"`
}

// Trade represents a completed round-trip trade (entry + exit).
// Side is the entry direction: SideBuy = long entry, SideSell = short entry.
type Trade struct {
	HandID         string          `json:"hand_id"`
	Symbol         string          `json:"symbol"`
	Side           Side            `json:"side"`
	Qty            decimal.Decimal `json:"qty"`
	EntryPrice     decimal.Decimal `json:"entry_price"`
	ExitPrice      decimal.Decimal `json:"exit_price"`
	EntryTimestamp time.Time       `json:"entry_timestamp"`
	ExitTimestamp  time.Time       `json:"exit_timestamp"`
	PnL            decimal.Decimal `json:"pnl"`
	PnLPct         decimal.Decimal `json:"pnl_pct"`
}

// SyncedPosition is a position as received from an external sync source (exchange REST API).
//
// The margin/futures fields mirror exchange.ExchangePosition's additions —
// zero-valued for spot exchanges. Side uses the same string values as
// exchange.PositionSide ("long"/"short"/"net"/"") without importing that
// package: portfolio (Layer 1) stays decoupled from infra/exchange, same as
// today, for a 3-value enum that isn't worth the cross-layer import.
type SyncedPosition struct {
	Symbol   string
	Qty      decimal.Decimal
	AvgPrice decimal.Decimal
	CurPrice decimal.Decimal

	Side             string // "long" | "short" | "net" | "" (spot)
	Leverage         decimal.Decimal
	LiquidationPrice decimal.Decimal
	MarginMode       string // "cross" | "isolated" | ""
}

// Balance is a per-asset balance as last reported by the exchange REST sync —
// a read-only side-channel (see Portfolio.Balances). Mirrors
// exchange.AssetBalance's fields locally rather than importing that package,
// same reasoning as SyncedPosition.Side above: portfolio (Layer 1) stays
// decoupled from infra/exchange (Layer 0 concrete shapes).
type Balance struct {
	Asset         string
	Free          decimal.Decimal
	Locked        decimal.Decimal
	MarginBalance decimal.Decimal
	UnrealizedPnL decimal.Decimal
}

// Summary is a one-call snapshot of key portfolio metrics for API responses.
// Field semantics (see docs/metrics-and-reports.md for canonical definitions):
//
//	Cash             — free quote balance at the broker (decreased by entries, increased by exits)
//	Equity           — Cash + Σ position.Qty × position.CurrentPrice (MtM total)
//	DeployedCapital  — Σ position.Qty × position.AvgPrice (entry-cost basis of open positions)
//	UnrealizedPnL    — Σ position.UnrealizedPnL (MtM delta from entry)
//	RealizedPnL      — Σ closed-trade PnL since inception (gross, no fees)
//	AvailableCash    — alias of Cash, kept for backward-compat (FE should prefer Cash)
//
// Hand-allocation aware fields (AllocatedToHands / UnallocatedCapital) are NOT
// included here — they belong at the helm/handler layer where hand state lives.
type Summary struct {
	InitialCapital  decimal.Decimal `json:"initial_capital"`
	Cash            decimal.Decimal `json:"cash"`
	AvailableCash   decimal.Decimal `json:"available_cash"` // deprecated alias of Cash
	Equity          decimal.Decimal `json:"equity"`
	DeployedCapital decimal.Decimal `json:"deployed_capital"`
	UnrealizedPnL   decimal.Decimal `json:"unrealized_pnl"`
	RealizedPnL     decimal.Decimal `json:"realized_pnl"`
	TotalReturn     float64         `json:"total_return_pct"`
	CurrentDD       float64         `json:"current_drawdown_pct"`
	MaxDD           float64         `json:"max_drawdown_pct"`
	WinRate         float64         `json:"win_rate_pct"`
	TotalTrades     int             `json:"total_trades"`
	OpenPositions   int             `json:"open_positions"`
	DailyPnL        decimal.Decimal `json:"daily_pnl"`
	Positions       []Position      `json:"positions"`
}
