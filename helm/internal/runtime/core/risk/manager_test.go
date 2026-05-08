package risk_test

import (
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func newPort(capital float64) *portfolio.Portfolio {
	return portfolio.New(decimal.NewFromFloat(capital))
}

func longSignal(sym string, strength float64) *engine.SignalMsg {
	return &engine.SignalMsg{S: sym, Dir: "long", Strength: strength}
}

func closeSignal(sym string) *engine.SignalMsg {
	return &engine.SignalMsg{S: sym, Dir: "close", Strength: 1}
}

// applyLoss simulates a round-trip losing trade, reducing portfolio equity by ~pct.
// Buys qty shares at entryPrice, records equity peak, then sells at exitPrice.
func applyLoss(p *portfolio.Portfolio, sym string, qty int64, entryPrice, exitPrice float64) {
	now := time.Now()
	p.ApplyFill(portfolio.Fill{
		Timestamp: now,
		Symbol:    sym,
		Side:      portfolio.SideBuy,
		Qty:       decimal.NewFromInt(qty),
		Price:     decimal.NewFromFloat(entryPrice),
	})
	p.RecordEquity(now) // capture peak equity after buy (total equity unchanged by buy)
	p.ApplyFill(portfolio.Fill{
		Timestamp: now.Add(time.Minute),
		Symbol:    sym,
		Side:      portfolio.SideSell,
		Qty:       decimal.NewFromInt(qty),
		Price:     decimal.NewFromFloat(exitPrice),
	})
}

// ── IsHalted ─────────────────────────────────────────────────────────────────

func TestIsHalted_StartsUnhalted(t *testing.T) {
	m := risk.New(risk.DefaultConfig(), newPort(10_000))
	if m.IsHalted() {
		t.Fatal("manager should not be halted on creation")
	}
}

// ── ResetHalt ────────────────────────────────────────────────────────────────

func TestResetHalt_ClearsHaltedFlag(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.Config{
		MaxPositions:      10,
		MaxPositionPct:    0.50,
		DailyLossLimitPct: 1.0,  // disabled — won't fire
		MaxDrawdownPct:    0.05, // 5%
	}
	m := risk.New(cfg, p)

	// Buy 100 shares @ $100 = $10 000 (all cash).
	// Sell @ $94 → equity $9 400 → drawdown 6% > 5% → triggers halt.
	applyLoss(p, "AAPL", 100, 100, 94)

	ok, reason := m.Validate(longSignal("AAPL", 0.5))
	if ok {
		t.Fatalf("expected halt after max drawdown breach, got approved; reason=%q", reason)
	}

	if !m.IsHalted() {
		t.Fatal("IsHalted should return true after max drawdown breach")
	}

	m.ResetHalt()

	if m.IsHalted() {
		t.Fatal("IsHalted should return false after ResetHalt")
	}
}

func TestResetHalt_AlsoClearsDailyHalt(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.Config{
		MaxPositions:      10,
		MaxPositionPct:    0.50,
		DailyLossLimitPct: 0.01, // 1% daily loss limit — easy to breach
		MaxDrawdownPct:    1.0,  // disabled
	}
	m := risk.New(cfg, p)

	// Lose ~5% daily → triggers daily halt.
	applyLoss(p, "AAPL", 50, 100, 90)

	ok, _ := m.Validate(longSignal("AAPL", 0.5))
	if ok {
		t.Fatal("expected daily loss halt to fire")
	}

	m.ResetHalt()

	// ResetHalt clears the global halted flag; IsHalted returns false.
	// (The daily loss limit will re-trigger on the next Validate call if the
	// losses are still present — that is intentional and correct behaviour.
	// ResetHalt provides a one-shot manual override, not a permanent bypass.)
	if m.IsHalted() {
		t.Fatal("IsHalted should return false after ResetHalt (global halt flag)")
	}
}

// ── Validate — close signals ──────────────────────────────────────────────────

func TestValidate_CloseSignal_AlwaysApproved(t *testing.T) {
	// Even with MaxPositions=0 and a halted manager, close must pass.
	p := newPort(10_000)
	cfg := risk.Config{MaxPositions: 0}
	m := risk.New(cfg, p)

	ok, reason := m.Validate(closeSignal("AAPL"))
	if !ok {
		t.Fatalf("close signal must always be approved, got rejected: %s", reason)
	}
}

func TestValidate_CloseSignal_ApprovedWhenHalted(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.Config{
		MaxPositions:      10,
		MaxPositionPct:    0.50,
		DailyLossLimitPct: 1.0,
		MaxDrawdownPct:    0.05,
	}
	m := risk.New(cfg, p)
	applyLoss(p, "AAPL", 100, 100, 94) // trigger halt

	// Long entry blocked.
	ok, _ := m.Validate(longSignal("AAPL", 0.5))
	if ok {
		t.Fatal("long should be blocked when halted")
	}

	// Close still allowed.
	ok, reason := m.Validate(closeSignal("AAPL"))
	if !ok {
		t.Fatalf("close should be allowed even when halted, got: %s", reason)
	}
}

// ── Validate — MaxPositions ───────────────────────────────────────────────────

func TestValidate_MaxPositions_Zero_BlocksEntry(t *testing.T) {
	p := newPort(10_000)
	// DailyLossLimitPct=1.0 and MaxDrawdownPct=1.0 effectively disable those
	// guards so only the MaxPositions check fires.
	cfg := risk.Config{MaxPositions: 0, MaxPositionPct: 0.10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0}
	m := risk.New(cfg, p)

	ok, reason := m.Validate(longSignal("AAPL", 0.5))
	if ok {
		t.Fatal("expected entry blocked when MaxPositions=0")
	}
	if reason != "max positions reached" {
		t.Fatalf("expected 'max positions reached', got %q", reason)
	}
}

func TestValidate_MaxPositions_AllowsNewWhenBelowLimit(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.DefaultConfig()
	cfg.MaxPositions = 3
	m := risk.New(cfg, p)

	ok, _ := m.Validate(longSignal("AAPL", 0.5))
	if !ok {
		t.Fatal("expected entry allowed when position count below max")
	}
}

func TestValidate_MaxPositions_AllowsAddingToExistingPosition(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.DefaultConfig()
	cfg.MaxPositions = 1
	m := risk.New(cfg, p)

	// Open one position in AAPL (fills the slot).
	p.ApplyFill(portfolio.Fill{
		Timestamp: time.Now(),
		Symbol:    "AAPL",
		Side:      portfolio.SideBuy,
		Qty:       decimal.NewFromInt(1),
		Price:     decimal.NewFromInt(100),
	})

	// Adding to AAPL (existing position) must be allowed.
	ok, reason := m.Validate(longSignal("AAPL", 0.5))
	if !ok {
		t.Fatalf("adding to existing position should be allowed: %s", reason)
	}

	// Opening a new TSLA position when max=1 and AAPL fills it → blocked.
	ok, reason = m.Validate(longSignal("TSLA", 0.5))
	if ok {
		t.Fatal("expected new position blocked when max positions reached")
	}
	if reason != "max positions reached" {
		t.Fatalf("expected 'max positions reached', got %q", reason)
	}
}

// ── UpdateConfig ─────────────────────────────────────────────────────────────

func TestUpdateConfig_ChangesMaxPositionsImmediately(t *testing.T) {
	p := newPort(10_000)
	// Use high limit values so daily-loss / drawdown guards never fire.
	// MaxPositions=0 is the only active gate here.
	m := risk.New(risk.Config{MaxPositions: 0, MaxPositionPct: 0.10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0}, p)

	ok, _ := m.Validate(longSignal("AAPL", 0.5))
	if ok {
		t.Fatal("expected blocked with MaxPositions=0")
	}

	m.UpdateConfig(risk.Config{MaxPositions: 5, MaxPositionPct: 0.10, DailyLossLimitPct: 0.02, MaxDrawdownPct: 0.10})

	ok, _ = m.Validate(longSignal("AAPL", 0.5))
	if !ok {
		t.Fatal("expected allowed after MaxPositions raised to 5")
	}
}

func TestUpdateConfig_ReducesMaxPositionPctForSizing(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.DefaultConfig()
	cfg.MaxPositionPct = 0.20 // 20%
	m := risk.New(cfg, p)

	price := decimal.NewFromInt(100)

	// At 20% equity ($10 000) → $2000 / $100 = 20 shares at full strength.
	qty := m.Size(longSignal("AAPL", 1.0), price)
	if !qty.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("expected 20 shares at 20%% allocation, got %s", qty)
	}

	// Drop to 10%.
	m.UpdateConfig(risk.Config{MaxPositions: 5, MaxPositionPct: 0.10, DailyLossLimitPct: 0.02, MaxDrawdownPct: 0.10})

	qty = m.Size(longSignal("AAPL", 1.0), price)
	if !qty.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("expected 10 shares at 10%% allocation, got %s", qty)
	}
}

// ── Size ─────────────────────────────────────────────────────────────────────

func TestSize_ScalesByStrength(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.DefaultConfig()
	cfg.MaxPositionPct = 0.10 // 10% of $10 000 = $1 000
	m := risk.New(cfg, p)

	price := decimal.NewFromInt(100)

	// Strength 1.0 → $1 000 / $100 = 10 shares.
	qty := m.Size(longSignal("AAPL", 1.0), price)
	if !qty.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("expected 10 shares at strength 1.0, got %s", qty)
	}

	// Strength 0.5 → $500 / $100 = 5 shares.
	qty = m.Size(longSignal("AAPL", 0.5), price)
	if !qty.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("expected 5 shares at strength 0.5, got %s", qty)
	}

	// Strength 0.0 → 0 shares.
	qty = m.Size(longSignal("AAPL", 0.0), price)
	if !qty.IsZero() {
		t.Fatalf("expected 0 shares at strength 0.0, got %s", qty)
	}
}

func TestSize_ZeroPrice_ReturnsZero(t *testing.T) {
	p := newPort(10_000)
	m := risk.New(risk.DefaultConfig(), p)

	qty := m.Size(longSignal("AAPL", 1.0), decimal.Zero)
	if !qty.IsZero() {
		t.Fatalf("expected zero qty for zero price, got %s", qty)
	}
}

func TestSize_FractionalPriceAllowsFractionalQty(t *testing.T) {
	p := newPort(1_000)
	cfg := risk.Config{MaxPositions: 5, MaxPositionPct: 1.0} // 100% = $1000
	m := risk.New(cfg, p)

	// Price < $1 → fractional crypto allowed (no floor).
	price := decimal.NewFromFloat(0.5)
	qty := m.Size(longSignal("DOGE", 1.0), price)
	// $1000 / $0.50 = 2000 units
	expected := decimal.NewFromInt(2000)
	if !qty.Equal(expected) {
		t.Fatalf("expected %s units for crypto-priced asset, got %s", expected, qty)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestManager_ConcurrentUpdateConfig_NoRace(t *testing.T) {
	p := newPort(10_000)
	m := risk.New(risk.DefaultConfig(), p)

	var wg sync.WaitGroup
	iterations := 200

	// Writer goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			m.UpdateConfig(risk.Config{
				MaxPositions:      i%10 + 1,
				MaxPositionPct:    0.05,
				DailyLossLimitPct: 0.02,
				MaxDrawdownPct:    0.10,
			})
		}
	}()

	// Reader goroutines exercising every public method.
	for j := 0; j < 4; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			price := decimal.NewFromInt(100)
			for i := 0; i < iterations; i++ {
				m.Validate(longSignal("AAPL", 0.5))
				m.Size(longSignal("AAPL", 0.5), price)
				m.IsHalted()
			}
		}()
	}

	wg.Wait()
}
