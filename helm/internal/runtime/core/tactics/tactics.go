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
	Price       decimal.Decimal `json:"price"`        // current market price
	ATR         decimal.Decimal `json:"atr"`          // average true range (from herald ledger)
	Spread      float64         `json:"spread"`       // bid-ask spread; currently always 0 (not wired to L2)
	Volume      float64         `json:"volume"`       // recent volume; currently always 0
	PositionQty decimal.Decimal `json:"position_qty"` // current open position size (0 if flat)
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
	// Declared but not yet wired to the execution path.
	TrailingStop decimal.Decimal `json:"trailing_stop,omitempty"`

	// Slices is the number of sub-orders for TWAP execution.
	// Declared but not yet implemented.
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
	TIFGTC     TimeInForce = "gtc" // good till cancelled
	TIFIOC     TimeInForce = "ioc" // immediate-or-cancel
	TIFFOK     TimeInForce = "fok" // fill-or-kill
)

// ── SizingMode ────────────────────────────────────────────────────────────────

// SizingMode defines which algorithm the Tactician uses to calculate position size.
type SizingMode string

const (
	// SizingFixedFractional scales unit capital by signal confidence.
	// qty = (unit * confidence) / price
	SizingFixedFractional SizingMode = "fixed_fractional"

	// SizingFixedQty uses a literal base-asset quantity, ignoring capital math.
	// qty = FixedQty
	SizingFixedQty SizingMode = "fixed_qty"

	// SizingQuoteQty spends a fixed quote amount per trade (e.g. 1000 USDT).
	// Sets ExecutionPlan.QuoteQty; the exchange determines base qty at fill time.
	SizingQuoteQty SizingMode = "quote_qty"

	// SizingPercentEquity deploys unit capital without confidence scaling.
	// qty = unit / price
	SizingPercentEquity SizingMode = "percent_equity"

	// SizingVolatility sizes by ATR-based risk parity.
	// riskDollar = unit * RiskPerTradePct; qty = riskDollar / ATR
	// Produces zero if the signal carries no ATR.
	SizingVolatility SizingMode = "volatility"
)

// ── SizingConfig ──────────────────────────────────────────────────────────────

// SizingConfig holds all parameters for the Tactician's sizing algorithm.
// Translated from domain.PositionConfig by runtime.BuildHandComponents.
type SizingConfig struct {
	Mode SizingMode `json:"mode"`

	// AllocatedCapital is the hand's fixed capital budget in quote currency (e.g. USDT).
	// Zero → use full account equity.
	AllocatedCapital decimal.Decimal `json:"allocated_capital,omitempty"`

	// Per-trade unit: how much capital to deploy in a single entry.
	// UnitCapital (fixed) takes priority over UnitPct.
	// Both zero → fall back to MaxPositionPct × allocatedEquity.
	UnitCapital decimal.Decimal `json:"unit_capital,omitempty"`
	UnitPct     float64         `json:"unit_pct,omitempty"`

	// RiskPerTradePct is used only by SizingVolatility (e.g. 0.01 = 1%).
	RiskPerTradePct float64 `json:"risk_per_trade_pct"`

	// MaxPositionPct is the legacy fallback when UnitCapital and UnitPct are both zero.
	MaxPositionPct float64 `json:"max_position_pct"`

	// FixedQty is used only by SizingFixedQty.
	FixedQty decimal.Decimal `json:"fixed_qty"`

	// FixedQuoteQty is used only by SizingQuoteQty.
	FixedQuoteQty decimal.Decimal `json:"fixed_quote_qty"`
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
