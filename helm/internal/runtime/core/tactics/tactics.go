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
	Qty          decimal.Decimal `json:"qty"`                   // quantity to trade
	EntryType    EntryType       `json:"entry_type"`            // how to enter
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

// ── Sizing ─────────────────────────────────────────────────────────────────

// SizingMode defines how to calculate position size.
type SizingMode string

const (
	SizingFixedFractional SizingMode = "fixed_fractional" // % of equity per trade
	SizingFixedQty        SizingMode = "fixed_qty"        // fixed quantity
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

	RiskPerTradePct   float64         `json:"risk_per_trade_pct"`   // for volatility mode (e.g. 0.01 = 1% of unit capital at risk)
	MaxPositionPct    float64         `json:"max_position_pct"`     // legacy fallback unit size (% of allocated equity)
	FixedQty          decimal.Decimal `json:"fixed_qty"`            // for fixed_qty mode
	StopLossATRMult   float64         `json:"stop_loss_atr_mult"`   // stop loss = ATR * mult (e.g. 2.0); 0 = disabled
	TakeProfitATRMult float64         `json:"take_profit_atr_mult"` // take profit = ATR * mult; 0 = use 2x SL rule
	StopLossPct       float64         `json:"stop_loss_pct"`        // fixed stop as % of entry (0 = disabled)
	TakeProfitPct     float64         `json:"take_profit_pct"`      // fixed TP as % of entry (0 = disabled)
	TrailingStopPct   float64         `json:"trailing_stop_pct"`    // trailing stop as % of entry (0 = disabled); replaces StopLoss when set
	MaxBarsHeld       int             `json:"max_bars_held"`        // time-stop: close after N bars (0 = disabled); bot-managed
}

// DefaultSizingConfig returns sensible defaults.
func DefaultSizingConfig() SizingConfig {
	return SizingConfig{
		Mode:            SizingFixedFractional,
		RiskPerTradePct: 0.01,
		MaxPositionPct:  0.10,
		StopLossATRMult: 2.0,
	}
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
	plan.Qty = t.size(intent, ctx)

	// Entry type based on urgency.
	switch intent.Urgency {
	case strategy.UrgencyImmediate:
		plan.EntryType = EntryMarket
	case strategy.UrgencyNormal:
		plan.EntryType = EntryLimit
		plan.LimitPrice = t.limitPrice(plan.Side, ctx)
	case strategy.UrgencyPatient:
		plan.EntryType = EntryTWAP
		plan.Slices = 5 // split into 5 orders
		plan.Qty = plan.Qty.Div(decimal.NewFromInt(5))
	}

	// Stop loss, trailing stop, and take profit (only for entries).
	if intent.Action == strategy.ActionEnterLong || intent.Action == strategy.ActionEnterShort {
		if t.sizing.TrailingStopPct > 0 {
			// Trailing stop takes precedence over fixed stop loss.
			plan.TrailingStop = decimal.NewFromFloat(t.sizing.TrailingStopPct)
		} else {
			plan.StopLoss = t.stopLoss(plan.Side, ctx)
		}
		plan.TakeProfit = t.takeProfit(plan.Side, ctx)
	}

	return plan
}

// MaxBarsHeld returns the time-stop limit configured for this tactician (0 = disabled).
func (t *Tactician) MaxBarsHeld() int { return t.sizing.MaxBarsHeld }

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
		qty = t.sizing.FixedQty

	case SizingVolatility:
		// Risk-parity: risk $ per trade / (ATR * multiplier) = qty.
		// Risk dollar is RiskPerTradePct of unit capital (not total equity).
		if ctx.ATR.IsPositive() && t.sizing.StopLossATRMult > 0 {
			riskDollar := unit.Mul(decimal.NewFromFloat(t.sizing.RiskPerTradePct))
			stopDistance := ctx.ATR.Mul(decimal.NewFromFloat(t.sizing.StopLossATRMult))
			qty = riskDollar.Div(stopDistance)
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

	// For stocks, round to whole shares.
	if ctx.Price.GreaterThan(decimal.NewFromInt(1)) {
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

// stopLoss calculates stop loss price.
// Priority: ATR-based (when ATR available) → pct-based → 0 (disabled).
func (t *Tactician) stopLoss(side string, ctx MarketContext) decimal.Decimal {
	if ctx.ATR.IsPositive() && t.sizing.StopLossATRMult > 0 {
		distance := ctx.ATR.Mul(decimal.NewFromFloat(t.sizing.StopLossATRMult))
		if side == "buy" {
			return ctx.Price.Sub(distance)
		}
		return ctx.Price.Add(distance)
	}
	if t.sizing.StopLossPct > 0 {
		distance := ctx.Price.Mul(decimal.NewFromFloat(t.sizing.StopLossPct))
		if side == "buy" {
			return ctx.Price.Sub(distance)
		}
		return ctx.Price.Add(distance)
	}
	return decimal.Zero
}

// takeProfit calculates take profit price.
// Priority: explicit ATR mult → 2x SL ATR mult → pct-based → 0 (disabled).
func (t *Tactician) takeProfit(side string, ctx MarketContext) decimal.Decimal {
	if ctx.ATR.IsPositive() {
		mult := t.sizing.TakeProfitATRMult
		if mult <= 0 && t.sizing.StopLossATRMult > 0 {
			mult = t.sizing.StopLossATRMult * 2.0 // default 2:1 R:R
		}
		if mult > 0 {
			distance := ctx.ATR.Mul(decimal.NewFromFloat(mult))
			if side == "buy" {
				return ctx.Price.Add(distance)
			}
			return ctx.Price.Sub(distance)
		}
	}
	if t.sizing.TakeProfitPct > 0 {
		distance := ctx.Price.Mul(decimal.NewFromFloat(t.sizing.TakeProfitPct))
		if side == "buy" {
			return ctx.Price.Add(distance)
		}
		return ctx.Price.Sub(distance)
	}
	return decimal.Zero
}
