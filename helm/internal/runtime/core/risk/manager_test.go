package risk_test

import (
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
)

func newPort(capital float64) *portfolio.Portfolio {
	return portfolio.New(decimal.NewFromFloat(capital))
}

func entryIntent(sym string, strength float64) strategy.Intent {
	return strategy.Intent{
		Signal:  strategy.Signal{Symbol: sym, Direction: strategy.DirLong, Strength: strength},
		Action:  strategy.ActionEnterLong,
		Urgency: strategy.UrgencyNormal,
	}
}

func closeIntent(sym string) strategy.Intent {
	return strategy.Intent{
		Signal:  strategy.Signal{Symbol: sym, Direction: strategy.DirExit, Strength: 1.0},
		Action:  strategy.ActionExitLong,
		Urgency: strategy.UrgencyImmediate,
	}
}

func applyLoss(p *portfolio.Portfolio, sym string, qty int64, entryPrice, exitPrice float64) {
	now := time.Now()
	p.ApplyFill(portfolio.Fill{
		Timestamp: now,
		Symbol:    sym,
		Side:      portfolio.SideBuy,
		Qty:       decimal.NewFromInt(qty),
		Price:     decimal.NewFromFloat(entryPrice),
	})
	p.RecordEquity(now)
	p.ApplyFill(portfolio.Fill{
		Timestamp: now.Add(time.Minute),
		Symbol:    sym,
		Side:      portfolio.SideSell,
		Qty:       decimal.NewFromInt(qty),
		Price:     decimal.NewFromFloat(exitPrice),
	})
}

// buyFill opens/extends a long position (notional = qty×price) without closing it.
func buyFill(p *portfolio.Portfolio, sym string, qty int64, price float64) {
	now := time.Now()
	p.ApplyFill(portfolio.Fill{
		Timestamp: now,
		Symbol:    sym,
		Side:      portfolio.SideBuy,
		Qty:       decimal.NewFromInt(qty),
		Price:     decimal.NewFromFloat(price),
	})
	p.RecordEquity(now)
}

// ── Gross exposure ceiling ──────────────────────────────────────────────────────

func TestValidate_GrossExposureCeiling(t *testing.T) {
	pf := newPort(10_000)
	m := risk.New(risk.Config{MaxPositions: 10, MaxGrossExposurePct: 1.0, MaxDrawdownPct: 1.0, DailyLossLimitPct: 1.0}, pf) // gross ≤ equity

	// Flat: gross 0 < cap → entry approved.
	if ok, reason := m.Validate(entryIntent("BTCUSDT", 1.0), "h1"); !ok {
		t.Fatalf("flat account should approve entry, got blocked: %s", reason)
	}

	// Deploy full equity: 100 @ 100 = 10_000 notional == equity → at the ceiling.
	buyFill(pf, "BTCUSDT", 100, 100)

	// Pyramid add to the same symbol is now blocked by the gross ceiling (binds adds).
	if ok, reason := m.Validate(entryIntent("BTCUSDT", 1.0), "h1"); ok {
		t.Fatalf("add at gross ceiling should be blocked, got approved (reason=%q)", reason)
	}
	// A new symbol is likewise blocked.
	if ok, _ := m.Validate(entryIntent("ETHUSDT", 1.0), "h1"); ok {
		t.Fatal("new entry at gross ceiling should be blocked")
	}

	// Exit always passes regardless of exposure.
	if ok, reason := m.Validate(closeIntent("BTCUSDT"), "h1"); !ok {
		t.Fatalf("exit must always pass, got blocked: %s", reason)
	}
}

func TestValidate_GrossExposureLeverageAllowsStacking(t *testing.T) {
	pf := newPort(10_000)
	m := risk.New(risk.Config{MaxPositions: 10, MaxGrossExposurePct: 2.0, MaxDrawdownPct: 1.0, DailyLossLimitPct: 1.0}, pf) // up to 2× equity

	buyFill(pf, "BTCUSDT", 100, 100) // gross 10_000, cap 20_000 → still room
	if ok, reason := m.Validate(entryIntent("BTCUSDT", 1.0), "h1"); !ok {
		t.Fatalf("under 2× ceiling should allow pyramid add, got blocked: %s", reason)
	}
}

// ── IsHalted ──────────────────────────────────────────────────────────────────

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
		DailyLossLimitPct: 1.0,
		MaxDrawdownPct:    0.05,
	}
	m := risk.New(cfg, p)
	applyLoss(p, "AAPL", 100, 100, 94) // 6% drawdown > 5% limit

	ok, reason := m.Validate(entryIntent("AAPL", 0.5), "test-hand")
	if ok {
		t.Fatalf("expected halt after max drawdown breach; reason=%q", reason)
	}
	if !m.IsHalted() {
		t.Fatal("IsHalted should be true after max drawdown breach")
	}

	m.ResetHalt()
	if m.IsHalted() {
		t.Fatal("IsHalted should be false after ResetHalt")
	}
}

func TestResetHalt_AlsoClearsDailyHalt(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.Config{
		MaxPositions:      10,
		DailyLossLimitPct: 0.01,
		MaxDrawdownPct:    1.0,
	}
	m := risk.New(cfg, p)
	applyLoss(p, "AAPL", 50, 100, 90) // ~5% loss > 1% limit

	ok, _ := m.Validate(entryIntent("AAPL", 0.5), "test-hand")
	if ok {
		t.Fatal("expected daily loss halt to fire")
	}

	m.ResetHalt()
	if m.IsHalted() {
		t.Fatal("IsHalted should be false after ResetHalt")
	}
}

// TestResetHalt_RebasesPeak_NoImmediateRehalt locks the fix for the dead
// ResetPeakToCurrentEquity wiring: after a max-drawdown halt, ResetHalt must rebase the
// peak so the next entry is approved instead of re-halting from the stale high-water mark.
func TestResetHalt_RebasesPeak_NoImmediateRehalt(t *testing.T) {
	p := newPort(10_000)
	// Daily-loss disabled (1.0) so only the drawdown gate is in play.
	cfg := risk.Config{MaxPositions: 10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 0.05}
	m := risk.New(cfg, p)
	applyLoss(p, "AAPL", 100, 100, 94) // 6% drawdown > 5% → halt

	if ok, _ := m.Validate(entryIntent("AAPL", 0.5), "h1"); ok {
		t.Fatal("expected halt after drawdown breach")
	}
	m.ResetHalt()

	// Peak rebased to current equity → drawdown 0 → entry approved, not re-halted.
	if ok, reason := m.Validate(entryIntent("MSFT", 0.5), "h1"); !ok {
		t.Fatalf("entry should be approved after reset (peak rebased), got blocked: %s", reason)
	}
	if m.IsHalted() {
		t.Fatal("manager should not re-halt after reset when drawdown is rebased to 0")
	}
}

// ── Validate — exit intents ───────────────────────────────────────────────────

func TestValidate_CloseIntent_AlwaysApproved(t *testing.T) {
	p := newPort(10_000)
	m := risk.New(risk.Config{MaxPositions: 0}, p)

	ok, reason := m.Validate(closeIntent("AAPL"), "test-hand")
	if !ok {
		t.Fatalf("close intent must always be approved; got: %s", reason)
	}
}

func TestValidate_CloseIntent_ApprovedWhenHalted(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.Config{MaxPositions: 10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 0.05}
	m := risk.New(cfg, p)
	applyLoss(p, "AAPL", 100, 100, 94)

	ok, _ := m.Validate(entryIntent("AAPL", 0.5), "test-hand")
	if ok {
		t.Fatal("entry should be blocked when halted")
	}

	ok, reason := m.Validate(closeIntent("AAPL"), "test-hand")
	if !ok {
		t.Fatalf("close should be allowed even when halted; got: %s", reason)
	}
}

// ── Validate — MaxPositions ───────────────────────────────────────────────────

func TestValidate_MaxPositions_Zero_IsUnlimited(t *testing.T) {
	p := newPort(10_000)
	// All guards disabled (0) — fully permissive. MaxPositions=0 means no breadth cap.
	m := risk.New(risk.Config{}, p)

	ok, reason := m.Validate(entryIntent("AAPL", 0.5), "test-hand")
	if !ok {
		t.Fatalf("MaxPositions=0 must be unlimited (entry approved), got blocked: %s", reason)
	}
}

func TestValidate_MaxPositions_AllowsNewWhenBelowLimit(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.DefaultConfig()
	cfg.MaxPositions = 3
	m := risk.New(cfg, p)

	ok, _ := m.Validate(entryIntent("AAPL", 0.5), "test-hand")
	if !ok {
		t.Fatal("expected entry allowed when position count below max")
	}
}

func TestValidate_MaxPositions_AllowsAddingToExistingPosition(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.DefaultConfig()
	cfg.MaxPositions = 1
	m := risk.New(cfg, p)

	p.ApplyFill(portfolio.Fill{
		Timestamp: time.Now(),
		Symbol:    "AAPL",
		Side:      portfolio.SideBuy,
		Qty:       decimal.NewFromInt(1),
		Price:     decimal.NewFromInt(100),
	})

	ok, reason := m.Validate(entryIntent("AAPL", 0.5), "test-hand")
	if !ok {
		t.Fatalf("adding to existing position should be allowed: %s", reason)
	}

	ok, reason = m.Validate(entryIntent("TSLA", 0.5), "test-hand")
	if ok {
		t.Fatal("expected new position blocked when max positions reached")
	}
	if reason != "max positions reached" {
		t.Fatalf("expected 'max positions reached', got %q", reason)
	}
}

// ── Validate — MaxOrderRateLimit ──────────────────────────────────────────────

func TestValidate_MaxOrderRateLimit_HaltsWhenExceeded(t *testing.T) {
	p := newPort(10_000)
	cfg := risk.Config{
		MaxPositions:       10,
		DailyLossLimitPct:  1.0,
		MaxDrawdownPct:     1.0,
		MaxOrderRateLimit:  3,
		OrderRateWindowSec: 60,
	}
	m := risk.New(cfg, p)

	// First 3 entry intents are approved
	for i := 0; i < 3; i++ {
		ok, reason := m.Validate(entryIntent("AAPL", 0.5), "hand-1")
		if !ok {
			t.Fatalf("expected intent %d to be approved; got: %s", i, reason)
		}
	}

	// 4th entry intent exceeds limit, halts trading
	ok, reason := m.Validate(entryIntent("AAPL", 0.5), "hand-1")
	if ok {
		t.Fatal("expected 4th intent to be blocked by rate limit")
	}
	if reason != "order frequency limit exceeded" {
		t.Fatalf("expected rate limit error, got %q", reason)
	}

	if !m.IsHalted() {
		t.Fatal("expected manager to be halted after rate limit breach")
	}

	// Exits are still allowed even when halted by rate limit
	ok, reason = m.Validate(closeIntent("AAPL"), "hand-1")
	if !ok {
		t.Fatalf("expected close intent to be approved even when halted; got: %s", reason)
	}

	// Reset clears rate limit and halts
	m.ResetHalt()
	if m.IsHalted() {
		t.Fatal("expected manager to be unhalted after reset")
	}

	// Next entries are approved again
	ok, reason = m.Validate(entryIntent("AAPL", 0.5), "hand-1")
	if !ok {
		t.Fatalf("expected entry to be approved after reset; got: %s", reason)
	}
}

// ── UpdateConfig ─────────────────────────────────────────────────────────────

func TestUpdateConfig_ChangesMaxPositionsImmediately(t *testing.T) {
	p := newPort(10_000)
	m := risk.New(risk.Config{MaxPositions: 1}, p)
	buyFill(p, "AAPL", 1, 100) // 1 open unit → at the MaxPositions=1 limit

	if ok, _ := m.Validate(entryIntent("MSFT", 0.5), "test-hand"); ok {
		t.Fatal("expected new symbol blocked at MaxPositions=1")
	}

	m.UpdateConfig(risk.Config{MaxPositions: 5})
	ok, reason := m.Validate(entryIntent("MSFT", 0.5), "test-hand")
	if !ok {
		t.Fatalf("expected allowed after MaxPositions raised to 5: %s", reason)
	}
}

// ── Concurrency ───────────────────────────────────────────────────────────────

func TestManager_ConcurrentUpdateConfig_NoRace(t *testing.T) {
	p := newPort(10_000)
	m := risk.New(risk.DefaultConfig(), p)

	var wg sync.WaitGroup
	const iterations = 200

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			m.UpdateConfig(risk.Config{
				MaxPositions:      i%10 + 1,
				DailyLossLimitPct: 0.02,
				MaxDrawdownPct:    0.10,
			})
		}
	}()

	for j := 0; j < 4; j++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				m.Validate(entryIntent("AAPL", 0.5), "test-hand")
				m.IsHalted()
			}
		}()
	}

	wg.Wait()
}
