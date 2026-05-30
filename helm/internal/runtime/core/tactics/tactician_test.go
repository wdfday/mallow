package tactics

import (
	"testing"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/strategy"
)

// enterIntent builds a fixed_fractional long entry with the given confidence.
func enterIntent(confidence float64) strategy.Intent {
	return strategy.Intent{
		Signal:     strategy.Signal{Symbol: "BTCUSDT", Strength: confidence},
		Action:     strategy.ActionEnterLong,
		Urgency:    strategy.UrgencyImmediate,
		Confidence: confidence,
	}
}

// TestStrengthSizingToggle verifies that fixed_fractional sizing scales by signal
// confidence only when StrengthSizing is true; when false the entry is sized at the
// full unit allocation regardless of confidence.
func TestStrengthSizingToggle(t *testing.T) {
	ctx := MarketContext{Price: decimal.NewFromInt(100)}
	equity := decimal.NewFromInt(10000)

	newTact := func(apply bool) *Tactician {
		tact := New(SizingConfig{
			Mode:           SizingFixedFractional,
			StrengthSizing: apply,
			UnitPct:        0.10, // unit = 1000 → 10 base @ price 100
		})
		tact.UpdateEquity(equity)
		return tact
	}

	on := newTact(true)
	off := newTact(false)

	full := on.Plan(enterIntent(1.0), ctx).Qty
	onWeak := on.Plan(enterIntent(0.25), ctx).Qty
	offWeak := off.Plan(enterIntent(0.25), ctx).Qty

	// Sanity: full allocation is 1000/100 = 10.
	if !full.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("full allocation: want 10, got %s", full)
	}

	// StrengthSizing on: weak confidence scales down (0.25 * 10 = 2.5).
	if !onWeak.Equal(decimal.NewFromFloat(2.5)) {
		t.Fatalf("strength_sizing on, weak: want 2.5, got %s", onWeak)
	}

	// StrengthSizing off: weak confidence ignored → full unit.
	if !offWeak.Equal(full) {
		t.Fatalf("strength_sizing off, weak: want %s (full), got %s", full, offWeak)
	}

	// And the two must differ, proving the toggle has an effect.
	if onWeak.Equal(offWeak) {
		t.Fatalf("toggle had no effect: on=%s off=%s", onWeak, offWeak)
	}
}
