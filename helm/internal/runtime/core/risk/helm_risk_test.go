package risk_test

// Additional coverage for risk.Manager gates not fully exercised by the
// existing manager_test.go:
//
//   - MaxDrawdownPct standalone: fire + correct reason string
//   - DailyLossLimitPct standalone: fire + blocks for the rest of the day
//   - unitCounter injection: MaxPositions uses unitCounter when set
//   - per-hand rate-limit isolation: limit is per-handID, not global
//   - empty handID skips rate-limit gate
//   - all guards disabled by DefaultConfig
//   - Gate priority: global halt checked before daily halt
//   - Gate priority: daily halt checked before MaxPositions
//   - Validate returns exact reason strings expected by callers
//
// go test -v -run TestRisk ./internal/runtime/core/risk/ -count=1

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/portfolio"
	"mallow/helm/internal/runtime/core/risk"
	"mallow/helm/internal/runtime/core/strategy"
)

// ── helpers (local; mirrors signal_unit_test.go helpers for the risk package) ─

func newPortfolio(capital float64) *portfolio.Portfolio {
	return portfolio.New(decimal.NewFromFloat(capital))
}

func riskEntryIntent(sym string) strategy.Intent {
	return strategy.Intent{
		Signal:  strategy.Signal{Symbol: sym, Direction: strategy.DirLong, Strength: 1.0},
		Action:  strategy.ActionEnterLong,
		Urgency: strategy.UrgencyNormal,
	}
}

func riskExitIntent(sym string) strategy.Intent {
	return strategy.Intent{
		Signal:  strategy.Signal{Symbol: sym, Direction: strategy.DirExit, Strength: 1.0},
		Action:  strategy.ActionExitLong,
		Urgency: strategy.UrgencyImmediate,
	}
}

// applyRoundTrip opens and closes a position, causing a realized PnL of
// (exitPrice - entryPrice) × qty. Negative means loss.
func applyRoundTrip(p *portfolio.Portfolio, sym string, qty int64, entryPrice, exitPrice float64) {
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
		Timestamp: now.Add(time.Millisecond),
		Symbol:    sym,
		Side:      portfolio.SideSell,
		Qty:       decimal.NewFromInt(qty),
		Price:     decimal.NewFromFloat(exitPrice),
	})
}

// openLong puts a long position into the portfolio (not closed).
func openLong(p *portfolio.Portfolio, sym string, qty int64, price float64) {
	p.ApplyFill(portfolio.Fill{
		Timestamp: time.Now(),
		Symbol:    sym,
		Side:      portfolio.SideBuy,
		Qty:       decimal.NewFromInt(qty),
		Price:     decimal.NewFromFloat(price),
	})
	p.RecordEquity(time.Now())
}

// ── Gate 1b: MaxDrawdownPct ───────────────────────────────────────────────────

func TestRisk_MaxDrawdownPct_HaltsOnBreach(t *testing.T) {
	p := newPortfolio(10_000)
	cfg := risk.Config{
		MaxPositions:      10,
		DailyLossLimitPct: 1.0,  // disabled
		MaxDrawdownPct:    0.10, // 10%
	}
	m := risk.New(cfg, p)

	// Baseline: entry approved.
	if ok, reason := m.Validate(riskEntryIntent("BTCUSDT"), "h1"); !ok {
		t.Fatalf("fresh account: expected approve, got blocked: %s", reason)
	}

	// Cause a 12% drawdown: 10_000 → record peak → lose 1_200.
	applyRoundTrip(p, "BTCUSDT", 100, 100, 88) // 100×(88-100) = -1_200

	ok, reason := m.Validate(riskEntryIntent("BTCUSDT"), "h1")
	if ok {
		t.Fatal("expected halt after 12% drawdown breaching 10% limit")
	}
	if reason != "max drawdown breached" {
		t.Fatalf("expected reason %q, got %q", "max drawdown breached", reason)
	}
	if !m.IsHalted() {
		t.Fatal("IsHalted should be true after drawdown breach")
	}
}

func TestRisk_MaxDrawdownPct_Zero_IsDisabled(t *testing.T) {
	p := newPortfolio(10_000)
	// DailyLossLimitPct=0 (disabled) so a catastrophic loss doesn't trip the daily gate
	// before we can verify the drawdown gate is disabled.
	m := risk.New(risk.Config{MaxDrawdownPct: 0, MaxPositions: 10, DailyLossLimitPct: 0}, p)

	// Extreme loss; drawdown gate disabled → entry still approved.
	applyRoundTrip(p, "BTCUSDT", 100, 100, 1) // almost total loss

	if ok, reason := m.Validate(riskEntryIntent("ETHUSDT"), "h1"); !ok {
		t.Fatalf("drawdown=0 must be disabled; got blocked: %s", reason)
	}
}

// ── Gate 2: DailyLossLimitPct ─────────────────────────────────────────────────

func TestRisk_DailyLossLimitPct_HaltsForDay(t *testing.T) {
	p := newPortfolio(10_000)
	cfg := risk.Config{
		MaxPositions:      10,
		DailyLossLimitPct: 0.02, // 2% = 200 USDT
		MaxDrawdownPct:    1.0,  // disabled
	}
	m := risk.New(cfg, p)

	// 3% daily loss (300 USDT > 200 limit) → daily halt.
	applyRoundTrip(p, "BTCUSDT", 100, 100, 97) // -300 USDT

	ok, reason := m.Validate(riskEntryIntent("ETHUSDT"), "h1")
	if ok {
		t.Fatal("expected daily loss halt")
	}
	if reason != "daily loss limit breached" {
		t.Fatalf("expected reason %q, got %q", "daily loss limit breached", reason)
	}

	// Subsequent call should now hit Gate 2 (haltedDay) even without a new loss.
	ok, reason = m.Validate(riskEntryIntent("SOLUSDT"), "h1")
	if ok {
		t.Fatal("expected continued block after haltedDay set")
	}
	if reason != "trading halted: daily loss limit breached" {
		t.Fatalf("expected persistent halted reason, got %q", reason)
	}
}

func TestRisk_DailyLossLimitPct_Zero_IsDisabled(t *testing.T) {
	p := newPortfolio(10_000)
	m := risk.New(risk.Config{DailyLossLimitPct: 0, MaxPositions: 10, MaxDrawdownPct: 1.0}, p)

	applyRoundTrip(p, "AAPL", 100, 100, 50) // 50% loss

	if ok, reason := m.Validate(riskEntryIntent("MSFT"), "h1"); !ok {
		t.Fatalf("DailyLossLimitPct=0 must be disabled; got blocked: %s", reason)
	}
}

func TestRisk_DailyLoss_ExitAlwaysPasses(t *testing.T) {
	p := newPortfolio(10_000)
	m := risk.New(risk.Config{DailyLossLimitPct: 0.001, MaxDrawdownPct: 1.0, MaxPositions: 10}, p)
	applyRoundTrip(p, "AAPL", 100, 100, 99) // tiny loss triggers 0.1% limit

	m.Validate(riskEntryIntent("AAPL"), "h1") // trip the gate

	ok, reason := m.Validate(riskExitIntent("AAPL"), "h1")
	if !ok {
		t.Fatalf("exit must pass even when daily loss halted: %s", reason)
	}
}

// ── Gate 3: MaxPositions with unitCounter ─────────────────────────────────────

func TestRisk_UnitCounter_OverridesPortfolioPositionCount(t *testing.T) {
	p := newPortfolio(10_000)
	cfg := risk.Config{MaxPositions: 2, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0}
	m := risk.New(cfg, p)

	// Portfolio has 0 positions. unitCounter returns 2 (external units tracked).
	externalUnits := 2
	m.SetUnitCounter(func() int { return externalUnits })

	// No portfolio position for BTCUSDT → new entry checked against unit count.
	// unitCounter() = 2 == MaxPositions → block.
	ok, reason := m.Validate(riskEntryIntent("BTCUSDT"), "h1")
	if ok {
		t.Fatal("expected block when unitCounter() == MaxPositions")
	}
	if reason != "max positions reached" {
		t.Fatalf("expected %q, got %q", "max positions reached", reason)
	}

	// Lower to 1 → entry allowed.
	externalUnits = 1
	if ok, reason := m.Validate(riskEntryIntent("ETHUSDT"), "h1"); !ok {
		t.Fatalf("expected allowed when unitCounter()=1 < 2: %s", reason)
	}
}

func TestRisk_MaxPositions_NilUnitCounter_FallsBackToPortfolioLen(t *testing.T) {
	p := newPortfolio(10_000)
	m := risk.New(risk.Config{MaxPositions: 1, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0}, p)
	// No SetUnitCounter call → falls back to len(portfolio.Positions()).

	openLong(p, "AAPL", 1, 100) // 1 open position

	// New symbol (not AAPL) → portfolio has 1 position == MaxPositions=1 → block.
	ok, reason := m.Validate(riskEntryIntent("MSFT"), "h1")
	if ok {
		t.Fatal("expected block from portfolio fallback when at MaxPositions")
	}
	if reason != "max positions reached" {
		t.Fatalf("expected %q, got %q", "max positions reached", reason)
	}
}

// ── Gate 2.5: Order rate limit ─────────────────────────────────────────────────

func TestRisk_RateLimit_PerHandIsolation(t *testing.T) {
	p := newPortfolio(10_000)
	cfg := risk.Config{
		MaxPositions:       10,
		DailyLossLimitPct:  1.0,
		MaxDrawdownPct:     1.0,
		MaxOrderRateLimit:  2,
		OrderRateWindowSec: 60,
	}
	m := risk.New(cfg, p)

	// hand-1: fill 2 slots.
	m.Validate(riskEntryIntent("AAPL"), "hand-1")
	m.Validate(riskEntryIntent("AAPL"), "hand-1")

	// hand-2: independent counter → still has 2 slots free.
	if ok, reason := m.Validate(riskEntryIntent("AAPL"), "hand-2"); !ok {
		t.Fatalf("hand-2 rate limit should be independent from hand-1; got blocked: %s", reason)
	}

	// hand-1: 3rd attempt → exceeds limit → halt.
	ok, reason := m.Validate(riskEntryIntent("AAPL"), "hand-1")
	if ok {
		t.Fatal("expected hand-1 to be blocked after exceeding its rate limit")
	}
	if reason != "order frequency limit exceeded" {
		t.Fatalf("expected %q, got %q", "order frequency limit exceeded", reason)
	}
}

func TestRisk_RateLimit_EmptyHandID_Skipped(t *testing.T) {
	p := newPortfolio(10_000)
	cfg := risk.Config{
		MaxPositions:       10,
		DailyLossLimitPct:  1.0,
		MaxDrawdownPct:     1.0,
		MaxOrderRateLimit:  1,
		OrderRateWindowSec: 60,
	}
	m := risk.New(cfg, p)

	// Empty handID → rate limit not applied (no tracking bucket).
	for i := 0; i < 5; i++ {
		if ok, reason := m.Validate(riskEntryIntent("BTCUSDT"), ""); !ok {
			t.Fatalf("empty handID should skip rate limit gate, got blocked at i=%d: %s", i, reason)
		}
	}
}

// ── DefaultConfig: all guards disabled ────────────────────────────────────────

func TestRisk_DefaultConfig_AllGatesDisabled(t *testing.T) {
	p := newPortfolio(10_000)
	m := risk.New(risk.DefaultConfig(), p)

	// Massive loss — no gate should fire.
	applyRoundTrip(p, "BTC", 1000, 100, 1)

	for i := 0; i < 20; i++ {
		if ok, reason := m.Validate(riskEntryIntent("ETH"), "h"); !ok {
			t.Fatalf("DefaultConfig must disable all gates; blocked at i=%d: %s", i, reason)
		}
	}
	if m.IsHalted() {
		t.Fatal("DefaultConfig should not halt")
	}
}

// ── Gate priority ordering ─────────────────────────────────────────────────────

// TestRisk_GatePriority_GlobalHaltBeforeDaily ensures that once a global halt
// (m.halted) is set by a drawdown breach, Gate 1 fires on subsequent calls
// before the daily-halt check (Gate 2). The global halt persists across calls
// while the daily halt only lasts the calendar day.
func TestRisk_GatePriority_GlobalHaltBeforeDaily(t *testing.T) {
	p := newPortfolio(10_000)
	cfg := risk.Config{
		MaxPositions:      10,
		DailyLossLimitPct: 1.0,  // effectively disabled
		MaxDrawdownPct:    0.01, // 1%
	}
	m := risk.New(cfg, p)

	// Trip the drawdown gate on the first call → m.halted = true.
	applyRoundTrip(p, "X", 100, 100, 98) // 2% drawdown > 1% limit
	ok, reason := m.Validate(riskEntryIntent("X"), "h")
	if ok || reason != "max drawdown breached" {
		t.Fatalf("expected 'max drawdown breached' on first trip, got ok=%v reason=%q", ok, reason)
	}
	if !m.IsHalted() {
		t.Fatal("IsHalted should be true after drawdown breach")
	}

	// Second call: m.halted=true → Gate 1 fires before any other gate.
	ok, reason = m.Validate(riskEntryIntent("Y"), "h")
	if ok {
		t.Fatal("expected continued block after global halt")
	}
	if reason != "trading halted: max drawdown breached" {
		t.Fatalf("expected Gate 1 reason %q, got %q", "trading halted: max drawdown breached", reason)
	}
}

// TestRisk_GatePriority_DailyHaltBeforeMaxPositions ensures that daily halt
// fires before the MaxPositions check (Gate 2 before Gate 3).
func TestRisk_GatePriority_DailyHaltBeforeMaxPositions(t *testing.T) {
	p := newPortfolio(10_000)
	cfg := risk.Config{
		MaxPositions:      1, // would block on count check
		DailyLossLimitPct: 0.001,
		MaxDrawdownPct:    1.0,
	}
	m := risk.New(cfg, p)

	// Trip daily halt first.
	applyRoundTrip(p, "AAPL", 100, 100, 99)
	m.Validate(riskEntryIntent("AAPL"), "h1") // sets haltedDay

	// Next call: daily halt (Gate 2) should fire before MaxPositions (Gate 3).
	ok, reason := m.Validate(riskEntryIntent("TSLA"), "h1")
	if ok {
		t.Fatal("expected block")
	}
	if reason != "trading halted: daily loss limit breached" {
		t.Fatalf("expected daily halt reason, got %q", reason)
	}
}

// ── Exact reason strings (API contract) ───────────────────────────────────────

func TestRisk_ReasonStrings_ExactMatch(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(p *portfolio.Portfolio, m *risk.Manager)
		cfg        risk.Config
		wantReason string
	}{
		{
			name: "global_halt",
			cfg:  risk.Config{MaxPositions: 10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 0.01},
			setup: func(p *portfolio.Portfolio, m *risk.Manager) {
				applyRoundTrip(p, "X", 100, 100, 98)  // 2% drawdown > 1%
				m.Validate(riskEntryIntent("X"), "h") // trip it
			},
			wantReason: "trading halted: max drawdown breached",
		},
		{
			name: "daily_halted",
			cfg:  risk.Config{MaxPositions: 10, DailyLossLimitPct: 0.01, MaxDrawdownPct: 1.0},
			setup: func(p *portfolio.Portfolio, m *risk.Manager) {
				applyRoundTrip(p, "X", 100, 100, 98)
				m.Validate(riskEntryIntent("X"), "h") // trip daily
			},
			wantReason: "trading halted: daily loss limit breached",
		},
		{
			name: "max_positions_reached",
			cfg:  risk.Config{MaxPositions: 1, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0},
			setup: func(p *portfolio.Portfolio, m *risk.Manager) {
				openLong(p, "AAA", 1, 100) // 1 open position == MaxPositions
			},
			wantReason: "max positions reached",
		},
		{
			name: "max_gross_exposure_reached",
			cfg:  risk.Config{MaxPositions: 10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0, MaxGrossExposurePct: 1.0},
			setup: func(p *portfolio.Portfolio, m *risk.Manager) {
				openLong(p, "BTC", 100, 100) // 10_000 == equity → at ceiling
			},
			wantReason: "max gross exposure reached",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPortfolio(10_000)
			m := risk.New(tc.cfg, p)
			tc.setup(p, m)

			ok, reason := m.Validate(riskEntryIntent("NEW"), "h1")
			if ok {
				t.Fatal("expected block")
			}
			if reason != tc.wantReason {
				t.Fatalf("reason mismatch\n  want: %q\n  got:  %q", tc.wantReason, reason)
			}
		})
	}
}

// ── Gate 0.5: available cash adequacy ────────────────────────────────────────

// TestRisk_CashAdequacy_BlocksWhenDepleted verifies that new entries are blocked
// when the injected availableCashFn returns 0 (cash ≤ sum of hand budgets).
func TestRisk_CashAdequacy_BlocksWhenDepleted(t *testing.T) {
	p := newPortfolio(10_000)
	m := risk.New(risk.Config{MaxPositions: 10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0}, p)

	available := decimal.NewFromFloat(500)
	m.SetAvailableCashFn(func() decimal.Decimal { return available })

	// Cash positive → entry approved.
	if ok, reason := m.Validate(riskEntryIntent("BTCUSDT"), "h1"); !ok {
		t.Fatalf("expected approve when cash positive, got blocked: %s", reason)
	}

	// Cash drops to zero → entry blocked.
	available = decimal.Zero
	ok, reason := m.Validate(riskEntryIntent("ETHUSDT"), "h1")
	if ok {
		t.Fatal("expected block when available cash == 0")
	}
	if reason != "insufficient available cash" {
		t.Fatalf("expected %q, got %q", "insufficient available cash", reason)
	}
}

// TestRisk_CashAdequacy_ExitAlwaysPasses verifies that exits bypass the cash gate.
func TestRisk_CashAdequacy_ExitAlwaysPasses(t *testing.T) {
	p := newPortfolio(10_000)
	m := risk.New(risk.Config{MaxPositions: 10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0}, p)
	m.SetAvailableCashFn(func() decimal.Decimal { return decimal.Zero }) // cash depleted

	ok, reason := m.Validate(riskExitIntent("BTCUSDT"), "h1")
	if !ok {
		t.Fatalf("exit must bypass cash gate, got blocked: %s", reason)
	}
}

// TestRisk_CashAdequacy_NilFn_Disabled verifies that when SetAvailableCashFn is
// not called (nil), the gate is disabled and entries are not blocked.
func TestRisk_CashAdequacy_NilFn_Disabled(t *testing.T) {
	p := newPortfolio(10_000)
	m := risk.New(risk.Config{MaxPositions: 10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 1.0}, p)
	// No SetAvailableCashFn call → gate disabled.

	if ok, reason := m.Validate(riskEntryIntent("BTCUSDT"), "h1"); !ok {
		t.Fatalf("gate must be disabled when fn is nil, got blocked: %s", reason)
	}
}

// TestRisk_CashAdequacy_FiresBeforeGlobalHalt verifies Gate 0.5 priority:
// cash check fires before global halt check (Gate 1).
func TestRisk_CashAdequacy_FiresBeforeGlobalHalt(t *testing.T) {
	p := newPortfolio(10_000)
	cfg := risk.Config{MaxPositions: 10, DailyLossLimitPct: 1.0, MaxDrawdownPct: 0.01}
	m := risk.New(cfg, p)

	// Trip global halt first.
	applyRoundTrip(p, "X", 100, 100, 98)  // 2% drawdown > 1%
	m.Validate(riskEntryIntent("X"), "h") // sets m.halted = true

	// Now also set cash to zero.
	m.SetAvailableCashFn(func() decimal.Decimal { return decimal.Zero })

	ok, reason := m.Validate(riskEntryIntent("Y"), "h")
	if ok {
		t.Fatal("expected block")
	}
	// Gate 0.5 fires first → reason is cash, not global halt.
	if reason != "insufficient available cash" {
		t.Fatalf("expected cash gate reason (Gate 0.5 before Gate 1), got %q", reason)
	}
}
