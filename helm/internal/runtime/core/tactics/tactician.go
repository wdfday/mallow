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

// scaleStrength multiplies v by signal strength when StrengthSizing is enabled.
// Used only by notional modes; risk-based modes bypass this entirely.
func (t *Tactician) scaleStrength(v decimal.Decimal, sig strategy.Signal) decimal.Decimal {
	if !t.sizing.StrengthSizing {
		return v
	}
	return v.Mul(strengthFactor(sig))
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
		plan.QuoteQty = t.scaleStrength(t.sizing.FixedQuoteQty, intent.Signal)
		// Capital isolation: clamp QuoteQty to AvailableBudget for entries.
		if ctx.AvailableBudget.IsPositive() && plan.QuoteQty.GreaterThan(ctx.AvailableBudget) {
			plan.QuoteQty = ctx.AvailableBudget
		}
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
	// NOTE: This is a sizing-only placeholder; the execution engine (hand_runner) does not support partial exits yet.
	if intent.Action == strategy.ActionScaleOut {
		return ctx.PositionQty.Abs().Div(decimal.NewFromInt(2))
	}

	var qty decimal.Decimal

	switch t.sizing.Mode {
	case SizingFixedQty:
		// Fixed-qty bypasses all capital logic — return immediately without clamping.
		return t.scaleStrength(t.sizing.FixedQty, intent.Signal)

	case SizingPercentEquity:
		// Notional sizing: deploy UnitPct × equity, scaled by signal strength.
		// qty = unit * strength / price.
		unit := t.unitCapital()
		if !unit.IsPositive() {
			slog.Warn("tactics: percent_equity sizing skipped — unit_pct not set",
				"symbol", intent.Signal.Symbol)
			return decimal.Zero
		}
		qty = t.scaleStrength(unit, intent.Signal).Div(ctx.Price)

	default: // fixed_fractional (Ralph Vince) and volatility
		// NOTE: the risk-based modes deliberately do NOT scale by strength — their size
		// is fixed by the risk fraction and the stop, not by conviction.
		// Ralph Vince fixed fractional: risk a fixed fraction f of account equity per
		// trade; position size = (f × equity) / risk-per-unit, where risk-per-unit is the
		// stop distance per unit. This is NOT notional sizing — qty falls out of the stop:
		// a wide stop → small qty, a tight stop → large qty (capped below).
		//   fixed_fractional → stop from the signal's SL (|price − SL|), ATR as fallback.
		//   volatility       → force ATR as the stop (pure volatility parity).
		f := decimal.NewFromFloat(t.sizing.RiskPerTradePct)
		if !f.IsPositive() {
			slog.Warn("tactics: risk sizing skipped — risk_per_trade_pct not set",
				"symbol", intent.Signal.Symbol)
			return decimal.Zero
		}
		stopDist := t.stopDistance(intent.Signal, ctx)
		if !stopDist.IsPositive() {
			slog.Warn("tactics: risk sizing skipped — no stop distance (signal has no SL and no ATR)",
				"symbol", intent.Signal.Symbol, "price", ctx.Price)
			return decimal.Zero
		}
		qty = t.allocatedEquity().Mul(f).Div(stopDist)
	}

	// Notional ceiling — Ralph Vince can demand a large position on a tight stop, so cap
	// exposure at MaxPositionPct × equity (its documented meaning). Default 100% of equity
	// when unset; spot cash + AvailableBudget clamp it further below.
	capPct := decimal.NewFromInt(1)
	if t.sizing.MaxPositionPct > 0 {
		capPct = decimal.NewFromFloat(t.sizing.MaxPositionPct)
	}
	maxQty := t.allocatedEquity().Mul(capPct).Div(ctx.Price)
	if qty.GreaterThan(maxQty) {
		qty = maxQty
	}

	// Capital isolation cap: for allocated hands, never deploy more than the
	// remaining budget (allocatedCap + cumPnL − deployedCapital).
	// AvailableBudget is zero for shared-pool hands → no cap applies.
	if ctx.AvailableBudget.IsPositive() {
		budgetQty := ctx.AvailableBudget.Div(ctx.Price)
		if qty.GreaterThan(budgetQty) {
			qty = budgetQty
		}
	}

	if qty.IsNegative() {
		return decimal.Zero
	}
	return qty
}

// allocatedEquity returns the capital budget for this hand.
// totalEquity is always set by ProcessTrade via UpdateEquity before Plan is called:
//   - hands with an allocated budget receive realizedEquity (allocatedCap + closedPnL)
//   - shared-pool hands receive Portfolio.Equity()
func (t *Tactician) allocatedEquity() decimal.Decimal {
	return t.totalEquity
}

// strengthFactor returns the signal's conviction in [0,1], used to scale the notional
// sizing modes (percent_equity, fixed_qty, quote_qty). The risk-based modes
// (fixed_fractional, volatility) ignore it. Zero/unset strength is treated as full (1.0).
func strengthFactor(sig strategy.Signal) decimal.Decimal {
	s := sig.Strength
	if s <= 0 {
		return decimal.NewFromInt(1)
	}
	if s > 1 {
		s = 1
	}
	return decimal.NewFromFloat(s)
}

// stopDistance returns the per-unit risk (the stop distance) used by Ralph Vince
// fixed-fractional sizing. fixed_fractional prefers the signal's stop loss — an absolute
// SL (|price − SL|) or an offset SL (|offset|) — and falls back to ATR when the signal
// carries no stop. volatility forces ATR (the pure volatility-parity variant). Returns
// zero when neither a stop nor ATR is available (caller then skips the trade).
func (t *Tactician) stopDistance(sig strategy.Signal, ctx MarketContext) decimal.Decimal {
	if t.sizing.Mode != SizingVolatility {
		if sig.IsOffset {
			if d := sig.StopPrice.Abs(); d.IsPositive() {
				return d
			}
		} else if sig.StopPrice.IsPositive() {
			if d := ctx.Price.Sub(sig.StopPrice).Abs(); d.IsPositive() {
				return d
			}
		}
	}
	return ctx.ATR // fallback for fixed_fractional; the forced stop for volatility
}

// unitCapital returns the equity deployed per single entry order for percent_equity:
// UnitPct × allocated equity. Returns zero when UnitPct is unset (caller then skips the
// trade) — MaxPositionPct is a cap, never a size source, so it does not back-fill here.
func (t *Tactician) unitCapital() decimal.Decimal {
	if t.sizing.UnitPct <= 0 {
		return decimal.Zero
	}
	return t.allocatedEquity().Mul(decimal.NewFromFloat(t.sizing.UnitPct))
}

// limitPrice calculates a limit price slightly inside the current market price.
// Uses a fixed 0.1% offset; wire bid-ask spread here when L2 data is available.
// The result is rounded to the same number of decimal places as ctx.Price so it
// satisfies the exchange PRICE_FILTER tick-size constraint (e.g. ETHUSDT tick=0.01).
func (t *Tactician) limitPrice(side string, ctx MarketContext) decimal.Decimal {
	offset := ctx.Price.Mul(decimal.NewFromFloat(0.001))
	var price decimal.Decimal
	if side == "buy" {
		price = ctx.Price.Sub(offset)
	} else {
		price = ctx.Price.Add(offset)
	}
	// Quantize to the tick implied by the reference price (same decimal places).
	// e.g. ctx.Price = 2016.55 (2 dp) → 2014.53345 → 2014.53.
	prec := int32(0)
	if exp := ctx.Price.Exponent(); exp < 0 {
		prec = -exp
		if prec > 8 {
			prec = 8
		}
	}
	return price.Round(prec)
}
