// Package tactics decides HOW to execute a trade intent.
// It answers "how to trade?" — position sizing, entry method, exit rules.
// Tactics take a strategy Intent + market context and produce an ExecutionPlan.
package tactics

import (
	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/strategy"
)

// MarketContext provides current market data needed for tactical decisions.
type MarketContext struct {
	Price       decimal.Decimal `json:"price"`        // current market price
	ATR         decimal.Decimal `json:"atr"`          // average true range (volatility)
	Spread      float64         `json:"spread"`       // bid-ask spread (non-financial, display only)
	Volume      float64         `json:"volume"`       // recent volume
	PositionQty decimal.Decimal `json:"position_qty"` // current position size (0 if none)
}

// ExecutionPlan is the output of tactics — tells the executor exactly what to do.
type ExecutionPlan struct {
	Action       strategy.Action `json:"action"`
	Symbol       string          `json:"symbol"`
	Side         string          `json:"side"`                  // "buy" or "sell"
	Qty          decimal.Decimal `json:"qty"`                   // base asset quantity; zero when QuoteQty is set
	QuoteQty     decimal.Decimal `json:"quote_qty,omitempty"`   // quote asset quantity (e.g. 1000 USDT); mutually exclusive with Qty
	EntryType    EntryType       `json:"entry_type"`            // how to enter
	TIF          TimeInForce     `json:"tif,omitempty"`         // time-in-force for entry order
	LimitPrice   decimal.Decimal `json:"limit_price,omitempty"` // for limit orders
	StopLoss     decimal.Decimal `json:"stop_loss,omitempty"`   // fixed stop price (0 when trailing is set)
	TakeProfit   decimal.Decimal `json:"take_profit,omitempty"`
	TrailingStop decimal.Decimal `json:"trailing_stop,omitempty"` // trailing stop as fraction (e.g. 0.02); 0 = disabled
	Slices       int             `json:"slices,omitempty"`        // for TWAP: number of slices
}

// EntryType defines how to enter the market.
type EntryType string

const (
	EntryMarket EntryType = "market" // hit market price immediately
	EntryLimit  EntryType = "limit"  // place limit order
	EntryTWAP   EntryType = "twap"   // time-weighted average: split into slices
)

// TimeInForce controls how long an order stays active.
// Mirrors exchange.TimeInForce — kept local to avoid import cycle.
type TimeInForce string

const (
	TIFDefault TimeInForce = ""    // use exchange default
	TIFDay     TimeInForce = "day" // cancel at end of session
	TIFGTC     TimeInForce = "gtc" // good till cancelled
	TIFIOC     TimeInForce = "ioc" // immediate-or-cancel
	TIFFOK     TimeInForce = "fok" // fill-or-kill
)

// ── Sizing ─────────────────────────────────────────────────────────────────

// SizingMode defines how to calculate position size.
type SizingMode string

const (
	SizingFixedFractional SizingMode = "fixed_fractional" // % of equity per trade
	SizingFixedQty        SizingMode = "fixed_qty"        // fixed base quantity
	SizingQuoteQty        SizingMode = "quote_qty"        // fixed quote quantity (e.g. spend 1000 USDT per trade)
	SizingVolatility      SizingMode = "volatility"       // ATR-based sizing (risk parity)
	SizingPercentEquity   SizingMode = "percent_equity"   // fixed % of equity regardless of confidence
)

// SizingConfig holds parameters for position sizing and exit levels.
type SizingConfig struct {
	Mode SizingMode `json:"mode"`

	// Capital isolation — bot's share of the orchestrator equity.
	// AllocatedCapital (fixed) takes priority over AllocatedPct.
	// Zero values → use full orchestrator equity (no isolation).
	AllocatedCapital decimal.Decimal `json:"allocated_capital,omitempty"`
	AllocatedPct     float64         `json:"allocated_pct,omitempty"`

	// Position unit — capital deployed per single entry.
	// UnitCapital (fixed) takes priority over UnitPct.
	// Zero values → fall back to MaxPositionPct * allocatedEquity (legacy).
	UnitCapital decimal.Decimal `json:"unit_capital,omitempty"`
	UnitPct     float64         `json:"unit_pct,omitempty"`

	// MaxPositions limits concurrent open positions (default 1).
	MaxPositions int `json:"max_positions,omitempty"`

	RiskPerTradePct float64         `json:"risk_per_trade_pct"` // for volatility mode (e.g. 0.01 = 1% of unit capital at risk)
	MaxPositionPct  float64         `json:"max_position_pct"`   // legacy fallback unit size (% of allocated equity)
	FixedQty        decimal.Decimal `json:"fixed_qty"`          // for fixed_qty mode
	FixedQuoteQty   decimal.Decimal `json:"fixed_quote_qty"`    // for quote_qty mode (e.g. 1000 USDT per trade)

}

// DefaultSizingConfig returns sensible defaults.
func DefaultSizingConfig() SizingConfig {
	return SizingConfig{
		Mode:            SizingFixedFractional,
		RiskPerTradePct: 0.01,
		MaxPositionPct:  0.10,
	}
}

// ── Planner interface ──────────────────────────────────────────────────────

// Planner is the outbound port for position sizing. Satisfied by *Tactician.
type Planner interface {
	Plan(intent strategy.Intent, ctx MarketContext) ExecutionPlan
	UpdateEquity(equity decimal.Decimal)
}

// ── Tactician ──────────────────────────────────────────────────────────────

// Tactician converts strategy intents into executable plans.
type Tactician struct {
	sizing      SizingConfig
	totalEquity decimal.Decimal // orchestrator-level equity, updated externally
}

// New creates a Tactician with the given sizing config.
func New(sizing SizingConfig) *Tactician {
	return &Tactician{sizing: sizing}
}

// UpdateEquity updates the total orchestrator equity for sizing calculations.
func (t *Tactician) UpdateEquity(equity decimal.Decimal) {
	t.totalEquity = equity
}

// allocatedEquity returns the capital budget for this bot.
// Priority: AllocatedCapital (fixed) → AllocatedPct * totalEquity → totalEquity.
func (t *Tactician) allocatedEquity() decimal.Decimal {
	if t.sizing.AllocatedCapital.IsPositive() {
		return t.sizing.AllocatedCapital
	}
	if t.sizing.AllocatedPct > 0 {
		return t.totalEquity.Mul(decimal.NewFromFloat(t.sizing.AllocatedPct))
	}
	return t.totalEquity
}

// unitCapital returns the capital deployed per single entry.
// Priority: UnitCapital (fixed) → UnitPct * allocatedEquity → MaxPositionPct * allocatedEquity (legacy).
func (t *Tactician) unitCapital() decimal.Decimal {
	alloc := t.allocatedEquity()
	if t.sizing.UnitCapital.IsPositive() {
		return t.sizing.UnitCapital
	}
	if t.sizing.UnitPct > 0 {
		return alloc.Mul(decimal.NewFromFloat(t.sizing.UnitPct))
	}
	// legacy fallback
	return alloc.Mul(decimal.NewFromFloat(t.sizing.MaxPositionPct))
}

// Plan converts a strategy intent + market context into an execution plan.
func (t *Tactician) Plan(intent strategy.Intent, ctx MarketContext) ExecutionPlan {
	plan := ExecutionPlan{
		Action: intent.Action,
		Symbol: intent.Signal.Symbol,
	}

	// Map action to side.
	switch intent.Action {
	case strategy.ActionEnterLong, strategy.ActionScaleIn:
		plan.Side = "buy"
	case strategy.ActionEnterShort, strategy.ActionExitLong, strategy.ActionScaleOut:
		plan.Side = "sell"
	case strategy.ActionExitShort:
		plan.Side = "buy" // cover short
	default:
		return plan
	}

	// Size the position.
	if t.sizing.Mode == SizingQuoteQty && t.sizing.FixedQuoteQty.IsPositive() &&
		(intent.Action == strategy.ActionEnterLong || intent.Action == strategy.ActionEnterShort || intent.Action == strategy.ActionScaleIn) {
		plan.QuoteQty = t.sizing.FixedQuoteQty
	} else {
		plan.Qty = t.size(intent, ctx)
	}

	// Entry type and TIF based on urgency.
	switch intent.Urgency {
	case strategy.UrgencyImmediate:
		plan.EntryType = EntryMarket
		plan.TIF = TIFDefault // exchange default; IOC/FOK for futures is set by adapter
	case strategy.UrgencyNormal:
		plan.EntryType = EntryLimit
		plan.TIF = TIFGTC
		plan.LimitPrice = t.limitPrice(plan.Side, ctx)
	case strategy.UrgencyPatient:
		plan.EntryType = EntryTWAP
		plan.TIF = TIFGTC
		plan.Slices = 5 // split into 5 orders
		plan.Qty = plan.Qty.Div(decimal.NewFromInt(5))
	}

	return plan
}

// size calculates position quantity based on sizing mode.
func (t *Tactician) size(intent strategy.Intent, ctx MarketContext) decimal.Decimal {
	if ctx.Price.IsZero() || t.totalEquity.IsZero() {
		return decimal.Zero
	}

	// For exits, close entire position.
	if intent.Action == strategy.ActionExitLong || intent.Action == strategy.ActionExitShort {
		return ctx.PositionQty.Abs()
	}

	// Scale-out: close half.
	if intent.Action == strategy.ActionScaleOut {
		return ctx.PositionQty.Abs().Div(decimal.NewFromInt(2))
	}

	unit := t.unitCapital()
	var qty decimal.Decimal

	switch t.sizing.Mode {
	case SizingFixedQty:
		// Fixed qty is authoritative — skip capital-based clamp and floor.
		return t.sizing.FixedQty

	case SizingVolatility:
		// Risk-parity: risk $ = RiskPerTradePct * unit; stop_distance = 1× ATR.
		// Script embeds stop_price directly in the signal; ATR here is just for sizing.
		if ctx.ATR.IsPositive() {
			riskDollar := unit.Mul(decimal.NewFromFloat(t.sizing.RiskPerTradePct))
			qty = riskDollar.Div(ctx.ATR)
		}

	case SizingPercentEquity:
		// Deploy full unit capital, no confidence scaling.
		qty = unit.Div(ctx.Price)

	default: // fixed_fractional
		// Scale unit capital by signal confidence.
		alloc := unit.Mul(decimal.NewFromFloat(intent.Confidence))
		qty = alloc.Div(ctx.Price)
	}

	// Clamp to unit capital (never deploy more than one unit per entry).
	maxQty := unit.Div(ctx.Price)
	if qty.GreaterThan(maxQty) {
		qty = maxQty
	}

	// For stocks, round to whole shares (only when qty ≥ 1 — crypto lots < 1 must not be floored).
	if qty.GreaterThanOrEqual(decimal.NewFromInt(1)) {
		qty = qty.Floor()
	}

	if qty.IsNegative() {
		return decimal.Zero
	}

	return qty
}

// limitPrice calculates a limit price slightly better than market.
func (t *Tactician) limitPrice(side string, ctx MarketContext) decimal.Decimal {
	spread := decimal.NewFromFloat(ctx.Spread)
	offset := spread.Mul(decimal.NewFromFloat(0.3)) // aim for 30% of spread improvement
	minOffset := ctx.Price.Mul(decimal.NewFromFloat(0.001))
	if offset.LessThan(minOffset) {
		offset = minOffset // at least 0.1% from market
	}
	if side == "buy" {
		return ctx.Price.Sub(offset) // buy below market
	}
	return ctx.Price.Add(offset) // sell above market
}
