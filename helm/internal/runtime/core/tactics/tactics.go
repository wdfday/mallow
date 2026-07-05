// Package tactics decides HOW to execute a trade intent.
// It answers "how to trade?" — position sizing, entry method, and TIF.
// The Tactician takes a strategy.Intent + MarketContext and produces an ExecutionPlan
// that tells the executor exactly what order(s) to place.
//
// Separation of concerns:
//   - strategy: what action to take (enter long, exit short, …)
//   - tactics:  how large, what entry type, what price
//   - risk:     whether to allow the trade at all
//
// The Planner interface is the outbound port consumed by HelmRuntime.ProcessTrade.
// *Tactician is the only implementation; inject a mock via the interface for tests.
package tactics

import (
	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/strategy"
)

// ── MarketContext ─────────────────────────────────────────────────────────────

// MarketContext carries the current market data needed for tactical decisions.
type MarketContext struct {
	Price decimal.Decimal `json:"price"` // current market price
	ATR   decimal.Decimal `json:"atr"`   // average true range (from herald ledger)
	// Volume is recent volume; always 0 — not yet wired to any data source.
	// Reserved for future volume-weighted sizing or urgency heuristics.
	Volume      float64         `json:"volume"`
	PositionQty decimal.Decimal `json:"position_qty"` // current open position size (0 if flat)
	// AvailableBudget is the per-hand cap on entry notional. When positive the
	// tactician clamps qty so qty*price ≤ AvailableBudget. Zero = no cap (legacy /
	// shared-pool hands rely on helm-level risk guards instead).
	AvailableBudget decimal.Decimal `json:"available_budget,omitzero"`
}

// ── ExecutionPlan ─────────────────────────────────────────────────────────────

// ExecutionPlan is the output of tactics — the exact instruction for the executor.
// Either Qty (base asset) or QuoteQty (quote asset) is set, never both.
type ExecutionPlan struct {
	Action     strategy.Action `json:"action"`
	Symbol     string          `json:"symbol"`
	Side       string          `json:"side"`       // "buy" or "sell"
	Qty        decimal.Decimal `json:"qty"`        // base-asset quantity; zero when QuoteQty is set
	QuoteQty   decimal.Decimal `json:"quote_qty"`  // quote-asset spend (e.g. 1000 USDT); mutually exclusive with Qty
	EntryType  EntryType       `json:"entry_type"` // how to enter
	TIF        TimeInForce     `json:"tif"`        // time-in-force for entry order
	LimitPrice decimal.Decimal `json:"limit_price,omitempty"`
	StopLoss   decimal.Decimal `json:"stop_loss,omitempty"`
	TakeProfit decimal.Decimal `json:"take_profit,omitempty"`

	// TrailingStop as a fraction (e.g. 0.02 = 2%); 0 = disabled.
	// Reserved — not yet wired to the execution path. See design-twap.md.
	TrailingStop decimal.Decimal `json:"trailing_stop,omitempty"`

	// Slices is the number of sub-orders for TWAP execution.
	// Reserved — not yet implemented. See design-twap.md for the TwapExecutor design.
	Slices int `json:"slices,omitempty"`
}

// ── EntryType ─────────────────────────────────────────────────────────────────

// EntryType defines how to enter the market.
type EntryType string

const (
	EntryMarket EntryType = "market" // hit market price immediately
	EntryLimit  EntryType = "limit"  // place a passive limit order
	EntryTWAP   EntryType = "twap"   // split into time-weighted slices (not yet implemented)
)

// ── TimeInForce ───────────────────────────────────────────────────────────────

// TimeInForce controls how long an order stays active.
// Mirrors exchange.TimeInForce — kept local to avoid an import cycle.
type TimeInForce string

const (
	TIFDefault TimeInForce = ""    // use exchange default
	TIFDay     TimeInForce = "day" // cancel at end of trading session
	TIFGTC     TimeInForce = "gtc" // good till canceled
	TIFIOC     TimeInForce = "ioc" // immediate-or-cancel
	TIFFOK     TimeInForce = "fok" // fill-or-kill
)

// ── SizingMode ────────────────────────────────────────────────────────────────

// SizingMode defines which algorithm the Tactician uses to calculate position size.
type SizingMode string

const (
	// SizingFixedFractional is Ralph Vince fixed fractional: risk a fixed fraction f of
	// equity per trade, sized off the stop distance.
	//   qty = (RiskPerTradePct * equity) / stopDistance
	// stopDistance comes from the signal's SL (|price-SL| absolute, |offset| if IsOffset),
	// falling back to ATR. Capped at MaxPositionPct * equity (default 100%).
	SizingFixedFractional SizingMode = "fixed_fractional"

	// SizingFixedQty uses a literal base-asset quantity, ignoring capital math.
	// qty = FixedQty
	SizingFixedQty SizingMode = "fixed_qty"

	// SizingQuoteQty spends a fixed quote amount per trade (e.g. 1000 USDT).
	// Sets ExecutionPlan.QuoteQty; the exchange determines base qty at fill time.
	SizingQuoteQty SizingMode = "quote_qty"

	// SizingPercentEquity is plain notional sizing: deploy a fraction of equity per unit.
	// qty = (UnitPct × equity × strength) / price. For absolute USDT notional, use quote_qty.
	SizingPercentEquity SizingMode = "percent_equity"

	// SizingVolatility is fixed fractional with the stop forced to ATR (volatility parity):
	//   qty = (RiskPerTradePct * equity) / ATR
	// i.e. SizingFixedFractional that ignores the signal SL. Zero if the signal carries no ATR.
	SizingVolatility SizingMode = "volatility"
)

// ── SizingConfig ──────────────────────────────────────────────────────────────

// SizingConfig holds all parameters for the Tactician's sizing algorithm.
// Translated from domain.PositionConfig by runtime.BuildHandComponents.
type SizingConfig struct {
	Mode SizingMode `json:"mode"`

	// UnitPct is the fraction of allocated equity deployed per entry unit.
	// Used only by SizingPercentEquity (e.g. 0.10 = 10% of equity per unit).
	UnitPct float64 `json:"unit_pct,omitempty"`

	// RiskPerTradePct is used by SizingFixedFractional and SizingVolatility (e.g. 0.01 = 1%).
	RiskPerTradePct float64 `json:"risk_per_trade_pct"`

	// FixedQty is used only by SizingFixedQty.
	FixedQty decimal.Decimal `json:"fixed_qty"`

	// FixedQuoteQty is used only by SizingQuoteQty.
	FixedQuoteQty decimal.Decimal `json:"fixed_quote_qty"`

	// StrengthSizing: when true, notional modes multiply size by signal strength.
	// Defaults to true; set false to trade at full declared size regardless of confidence.
	// Risk-based modes (fixed_fractional, volatility) always ignore this.
	StrengthSizing bool `json:"strength_sizing"`

	// MaxPositionPct is the notional exposure ceiling as a fraction of equity (cap only —
	// it never sources position size). Zero → default 100% of equity.
	MaxPositionPct float64 `json:"max_position_pct"`
}

// DefaultSizingConfig returns conservative defaults (fixed_fractional, 10% unit).
func DefaultSizingConfig() SizingConfig {
	return SizingConfig{
		Mode:            SizingFixedFractional,
		RiskPerTradePct: 0.01,
		MaxPositionPct:  0.10,
	}
}

// ── Planner interface ─────────────────────────────────────────────────────────

// Planner is the outbound port consumed by HelmRuntime.ProcessTrade.
// Satisfied by *Tactician; inject a stub for unit tests.
type Planner interface {
	Plan(intent strategy.Intent, ctx MarketContext) ExecutionPlan
	UpdateEquity(equity decimal.Decimal)
}
