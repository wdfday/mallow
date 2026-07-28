package tactics

import (
	"testing"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/fleet/actor/core/strategy"
)

// longIntent builds a long entry with the given signal strength.
func longIntent(strength float64) strategy.Intent {
	return strategy.Intent{
		Signal:  strategy.Signal{Symbol: "BTCUSDT", Strength: strength},
		Action:  strategy.ActionEnterLong,
		Urgency: strategy.UrgencyImmediate,
	}
}

// TestPercentEquityScalesByStrength verifies that the notional modes (percent_equity)
// scale linearly with signal strength: strength 1.0 → full unit, 0.25 → quarter unit.
func TestPercentEquityScalesByStrength(t *testing.T) {
	ctx := MarketContext{Price: decimal.NewFromInt(100)}
	equity := decimal.NewFromInt(10000)

	tact := New(SizingConfig{
		Mode:           SizingPercentEquity,
		UnitPct:        0.10, // unit = 1000 → 10 base @ price 100
		StrengthSizing: true, // this test's whole point is strength scaling
	})
	tact.UpdateEquity(equity)

	full := tact.Plan(longIntent(1.0), ctx).Qty
	weak := tact.Plan(longIntent(0.25), ctx).Qty

	if !full.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("full allocation: want 10, got %s", full)
	}
	// 0.25 strength → 0.25 * 10 = 2.5.
	if !weak.Equal(decimal.NewFromFloat(2.5)) {
		t.Fatalf("weak strength: want 2.5, got %s", weak)
	}
}

// TestFixedFractionalIgnoresStrength verifies the Ralph Vince risk-based mode does NOT
// scale by signal strength — size is fixed by the risk fraction and the stop distance.
func TestFixedFractionalIgnoresStrength(t *testing.T) {
	// Price 100, SL 90 → stop distance 10. Risk 1% of 10k equity = 100.
	// qty = 100 / 10 = 10 (well under the MaxPositionPct cap of 100% → 100 units).
	ctx := MarketContext{Price: decimal.NewFromInt(100)}
	equity := decimal.NewFromInt(10000)

	tact := New(SizingConfig{
		Mode:            SizingFixedFractional,
		RiskPerTradePct: 0.01,
		MaxPositionPct:  1.0,
	})
	tact.UpdateEquity(equity)

	intent := func(strength float64) strategy.Intent {
		in := longIntent(strength)
		in.Signal.StopPrice = decimal.NewFromInt(90)
		return in
	}

	full := tact.Plan(intent(1.0), ctx).Qty
	weak := tact.Plan(intent(0.25), ctx).Qty

	if !full.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("fixed_fractional size: want 10, got %s", full)
	}
	// Strength must NOT change the size for risk-based modes.
	if !weak.Equal(full) {
		t.Fatalf("fixed_fractional must ignore strength: full=%s weak=%s", full, weak)
	}
}
