// Package tactics decides HOW to execute a trade intent.
// It answers "how to trade?" — position sizing, entry method, exit rules.
// Tactics take a strategy Intent + market context and produce an ExecutionPlan.
package tactics

import (
	"math"

	"orchestrator/internal/runtime/core/strategy"
)

// MarketContext provides current market data needed for tactical decisions.
type MarketContext struct {
	Price       float64 `json:"price"`        // current market price
	ATR         float64 `json:"atr"`          // average true range (volatility)
	Spread      float64 `json:"spread"`       // bid-ask spread
	Volume      float64 `json:"volume"`       // recent volume
	PositionQty float64 `json:"position_qty"` // current position size (0 if none)
}

// ExecutionPlan is the output of tactics — tells the executor exactly what to do.
type ExecutionPlan struct {
	Action       strategy.Action `json:"action"`
	Symbol       string          `json:"symbol"`
	Side         string          `json:"side"`                  // "buy" or "sell"
	Qty          float64         `json:"qty"`                   // quantity to trade
	EntryType    EntryType       `json:"entry_type"`            // how to enter
	LimitPrice   float64         `json:"limit_price,omitempty"` // for limit orders
	StopLoss     float64         `json:"stop_loss,omitempty"`   // fixed stop price (0 when trailing is set)
	TakeProfit   float64         `json:"take_profit,omitempty"`
	TrailingStop float64         `json:"trailing_stop,omitempty"` // trailing stop as fraction (e.g. 0.02); 0 = disabled
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
	Mode              SizingMode `json:"mode"`
	RiskPerTradePct   float64    `json:"risk_per_trade_pct"`   // for fixed_fractional (e.g. 0.01 = 1%)
	MaxPositionPct    float64    `json:"max_position_pct"`     // max % of equity in one position
	FixedQty          float64    `json:"fixed_qty"`            // for fixed_qty mode
	StopLossATRMult   float64    `json:"stop_loss_atr_mult"`   // stop loss = ATR * mult (e.g. 2.0); 0 = disabled
	TakeProfitATRMult float64    `json:"take_profit_atr_mult"` // take profit = ATR * mult; 0 = use 2x SL rule
	StopLossPct       float64    `json:"stop_loss_pct"`        // fixed stop as % of entry (0 = disabled)
	TakeProfitPct     float64    `json:"take_profit_pct"`      // fixed TP as % of entry (0 = disabled)
	TrailingStopPct   float64    `json:"trailing_stop_pct"`    // trailing stop as % of entry (0 = disabled); replaces StopLoss when set
	MaxBarsHeld       int        `json:"max_bars_held"`        // time-stop: close after N bars (0 = disabled); bot-managed
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
	sizing SizingConfig
	equity float64 // current equity, updated externally
}

// New creates a Tactician with the given sizing config.
func New(sizing SizingConfig) *Tactician {
	return &Tactician{sizing: sizing}
}

// UpdateEquity updates the current equity for sizing calculations.
func (t *Tactician) UpdateEquity(equity float64) {
	t.equity = equity
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
		plan.Qty = plan.Qty / 5.0
	}

	// Stop loss, trailing stop, and take profit (only for entries).
	if intent.Action == strategy.ActionEnterLong || intent.Action == strategy.ActionEnterShort {
		if t.sizing.TrailingStopPct > 0 {
			// Trailing stop takes precedence over fixed stop loss.
			plan.TrailingStop = t.sizing.TrailingStopPct
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
func (t *Tactician) size(intent strategy.Intent, ctx MarketContext) float64 {
	if ctx.Price <= 0 || t.equity <= 0 {
		return 0
	}

	// For exits, close entire position.
	if intent.Action == strategy.ActionExitLong || intent.Action == strategy.ActionExitShort {
		return math.Abs(ctx.PositionQty)
	}

	// Scale-out: close half.
	if intent.Action == strategy.ActionScaleOut {
		return math.Abs(ctx.PositionQty) / 2.0
	}

	var qty float64

	switch t.sizing.Mode {
	case SizingFixedQty:
		qty = t.sizing.FixedQty

	case SizingVolatility:
		// Risk-parity: risk $ per trade / (ATR * multiplier) = qty
		if ctx.ATR > 0 && t.sizing.StopLossATRMult > 0 {
			riskDollar := t.equity * t.sizing.RiskPerTradePct
			stopDistance := ctx.ATR * t.sizing.StopLossATRMult
			qty = riskDollar / stopDistance
		}

	case SizingPercentEquity:
		// Fixed % of equity, no confidence scaling.
		alloc := t.equity * t.sizing.MaxPositionPct
		qty = alloc / ctx.Price

	default: // fixed_fractional
		alloc := t.equity * t.sizing.MaxPositionPct
		alloc *= intent.Confidence // scale by confidence
		qty = alloc / ctx.Price
	}

	// Clamp to max position size.
	maxQty := (t.equity * t.sizing.MaxPositionPct) / ctx.Price
	if qty > maxQty {
		qty = maxQty
	}

	// For stocks, round to whole shares.
	if ctx.Price > 1.0 {
		qty = math.Floor(qty)
	}

	if qty < 0 {
		qty = 0
	}

	return qty
}

// limitPrice calculates a limit price slightly better than market.
func (t *Tactician) limitPrice(side string, ctx MarketContext) float64 {
	offset := ctx.Spread * 0.3 // aim for 30% of spread improvement
	if offset < ctx.Price*0.001 {
		offset = ctx.Price * 0.001 // at least 0.1% from market
	}
	if side == "buy" {
		return ctx.Price - offset // buy below market
	}
	return ctx.Price + offset // sell above market
}

// stopLoss calculates stop loss price.
// Priority: ATR-based (when ATR available) → pct-based → 0 (disabled).
func (t *Tactician) stopLoss(side string, ctx MarketContext) float64 {
	if ctx.ATR > 0 && t.sizing.StopLossATRMult > 0 {
		distance := ctx.ATR * t.sizing.StopLossATRMult
		if side == "buy" {
			return ctx.Price - distance
		}
		return ctx.Price + distance
	}
	if t.sizing.StopLossPct > 0 {
		distance := ctx.Price * t.sizing.StopLossPct
		if side == "buy" {
			return ctx.Price - distance
		}
		return ctx.Price + distance
	}
	return 0
}

// takeProfit calculates take profit price.
// Priority: explicit ATR mult → 2x SL ATR mult → pct-based → 0 (disabled).
func (t *Tactician) takeProfit(side string, ctx MarketContext) float64 {
	if ctx.ATR > 0 {
		mult := t.sizing.TakeProfitATRMult
		if mult <= 0 && t.sizing.StopLossATRMult > 0 {
			mult = t.sizing.StopLossATRMult * 2.0 // default 2:1 R:R
		}
		if mult > 0 {
			distance := ctx.ATR * mult
			if side == "buy" {
				return ctx.Price + distance
			}
			return ctx.Price - distance
		}
	}
	if t.sizing.TakeProfitPct > 0 {
		distance := ctx.Price * t.sizing.TakeProfitPct
		if side == "buy" {
			return ctx.Price + distance
		}
		return ctx.Price - distance
	}
	return 0
}
