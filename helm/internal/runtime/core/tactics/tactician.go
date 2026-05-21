package tactics

import (
	"log/slog"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/strategy"
)

// Tactician converts strategy intents into executable plans.
// It holds account equity (updated externally) and a SizingConfig,
// and answers "how many shares/contracts, at what price, and how?"
type Tactician struct {
	sizing      SizingConfig
	totalEquity decimal.Decimal // updated via UpdateEquity on every portfolio sync
}

// New creates a Tactician with the given sizing config.
func New(sizing SizingConfig) *Tactician {
	return &Tactician{sizing: sizing}
}

// UpdateEquity sets the total account equity used for percentage-based sizing.
// Called by ProcessTrade before every Plan() invocation.
func (t *Tactician) UpdateEquity(equity decimal.Decimal) {
	t.totalEquity = equity
}

// Plan converts a strategy Intent + current MarketContext into an ExecutionPlan.
//
// Sizing:
//   - SizingQuoteQty (enter/scale-in only): sets plan.QuoteQty directly, skips size().
//   - All other modes: calls size() which returns a base-asset Qty.
//
// Entry type is derived from Intent.Urgency:
//   - Immediate → Market
//   - Normal    → Limit (price via limitPrice)
//   - Patient   → TWAP / 5 slices (declared; execution not yet implemented)
func (t *Tactician) Plan(intent strategy.Intent, ctx MarketContext) ExecutionPlan {
	plan := ExecutionPlan{
		Action: intent.Action,
		Symbol: intent.Signal.Symbol,
	}

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
		(intent.Action == strategy.ActionEnterLong ||
			intent.Action == strategy.ActionEnterShort ||
			intent.Action == strategy.ActionScaleIn) {
		plan.QuoteQty = t.sizing.FixedQuoteQty
	} else {
		plan.Qty = t.size(intent, ctx)
	}

	// Entry method and TIF from urgency.
	switch intent.Urgency {
	case strategy.UrgencyImmediate:
		plan.EntryType = EntryMarket
		plan.TIF = TIFDefault
	case strategy.UrgencyNormal:
		plan.EntryType = EntryLimit
		plan.TIF = TIFGTC
		plan.LimitPrice = t.limitPrice(plan.Side, ctx)
	case strategy.UrgencyPatient:
		plan.EntryType = EntryTWAP
		plan.TIF = TIFGTC
		plan.Slices = 5
		plan.Qty = plan.Qty.Div(decimal.NewFromInt(5))
	}

	return plan
}

// size calculates base-asset quantity for the given intent and market context.
// Returns zero if price or equity is zero, or if the mode produces a non-positive result.
func (t *Tactician) size(intent strategy.Intent, ctx MarketContext) decimal.Decimal {
	if ctx.Price.IsZero() || t.totalEquity.IsZero() {
		return decimal.Zero
	}

	// Exits close the entire current position.
	if intent.Action == strategy.ActionExitLong || intent.Action == strategy.ActionExitShort {
		return ctx.PositionQty.Abs()
	}

	// Scale-out closes half the current position.
	if intent.Action == strategy.ActionScaleOut {
		return ctx.PositionQty.Abs().Div(decimal.NewFromInt(2))
	}

	unit := t.unitCapital()
	var qty decimal.Decimal

	switch t.sizing.Mode {
	case SizingFixedQty:
		// Fixed-qty bypasses all capital logic — return immediately without clamping.
		return t.sizing.FixedQty

	case SizingVolatility:
		// Risk-parity: risk$ = RiskPerTradePct × unit; position size = risk$ / ATR.
		// ATR comes from the signal (herald ledger); if zero the hand does not trade.
		if ctx.ATR.IsPositive() {
			riskDollar := unit.Mul(decimal.NewFromFloat(t.sizing.RiskPerTradePct))
			qty = riskDollar.Div(ctx.ATR)
		} else {
			slog.Warn("tactics: volatility sizing skipped — signal carries no ATR",
				"symbol", intent.Signal.Symbol, "price", ctx.Price)
		}

	case SizingPercentEquity:
		// Deploy full unit capital, ignoring signal confidence.
		qty = unit.Div(ctx.Price)

	default: // fixed_fractional
		// Scale unit capital by signal confidence.
		alloc := unit.Mul(decimal.NewFromFloat(intent.Confidence))
		qty = alloc.Div(ctx.Price)
	}

	// Clamp to one unit (never deploy more than one unit per entry).
	maxQty := unit.Div(ctx.Price)
	if qty.GreaterThan(maxQty) {
		qty = maxQty
	}

	if qty.IsNegative() {
		return decimal.Zero
	}
	return qty
}

// allocatedEquity returns the capital budget for this hand.
// AllocatedCapital (fixed USDT) takes priority; zero means full account equity.
func (t *Tactician) allocatedEquity() decimal.Decimal {
	if t.sizing.AllocatedCapital.IsPositive() {
		return t.sizing.AllocatedCapital
	}
	return t.totalEquity
}

// unitCapital returns the capital deployed per single entry order.
// Priority: UnitCapital (fixed) → UnitPct × allocated → MaxPositionPct × allocated (legacy).
func (t *Tactician) unitCapital() decimal.Decimal {
	alloc := t.allocatedEquity()
	if t.sizing.UnitCapital.IsPositive() {
		return t.sizing.UnitCapital
	}
	if t.sizing.UnitPct > 0 {
		return alloc.Mul(decimal.NewFromFloat(t.sizing.UnitPct))
	}
	return alloc.Mul(decimal.NewFromFloat(t.sizing.MaxPositionPct))
}

// limitPrice calculates a limit price slightly inside the current market price.
// Aims for 30% of the bid-ask spread improvement, with a 0.1% minimum offset.
func (t *Tactician) limitPrice(side string, ctx MarketContext) decimal.Decimal {
	spread := decimal.NewFromFloat(ctx.Spread)
	offset := spread.Mul(decimal.NewFromFloat(0.3))
	minOffset := ctx.Price.Mul(decimal.NewFromFloat(0.001))
	if offset.LessThan(minOffset) {
		offset = minOffset
	}
	if side == "buy" {
		return ctx.Price.Sub(offset)
	}
	return ctx.Price.Add(offset)
}
