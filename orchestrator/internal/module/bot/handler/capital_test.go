package handler

// checkCapitalAllocation is an unexported package-level function, so tests
// live in the same package (white-box, no _test suffix on package name).

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"orchestrator/internal/module/bot/domain"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func orchWithCapital(capital float64) *orchdomain.OrchestratorConfig {
	return &orchdomain.OrchestratorConfig{
		ID:      uuid.New(),
		Capital: capital,
	}
}

func botSummaryUSD(id string, allocated float64) domain.BotSummary {
	return domain.BotSummary{
		ID: id,
		Position: domain.PositionConfig{
			AllocatedCapital: decimal.NewFromFloat(allocated),
		},
	}
}

func botSummaryPct(id string, allocatedPct float64) domain.BotSummary {
	return domain.BotSummary{
		ID: id,
		Position: domain.PositionConfig{
			AllocatedPct: allocatedPct,
		},
	}
}

func posUSD(v float64) domain.PositionConfig {
	return domain.PositionConfig{AllocatedCapital: decimal.NewFromFloat(v)}
}

func posPct(v float64) domain.PositionConfig {
	return domain.PositionConfig{AllocatedPct: v}
}

// ── No allocation requested — always passes ───────────────────────────────────

func TestCheckCapitalAllocation_NoAllocation_Passes(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryUSD("bot1", 5_000),
	}
	if err := checkCapitalAllocation(orch, bots, domain.PositionConfig{}, ""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// ── Fixed-USD allocation ─────────────────────────────────────────────────────

func TestCheckCapitalAllocation_USD_WithinBudget(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryUSD("bot1", 3_000),
		botSummaryUSD("bot2", 3_000),
	}
	// 4 000 requested, 4 000 available → OK.
	if err := checkCapitalAllocation(orch, bots, posUSD(4_000), ""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckCapitalAllocation_USD_ExceedsBudget(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryUSD("bot1", 6_000),
		botSummaryUSD("bot2", 3_000),
	}
	// 9 000 used, only 1 000 left; requesting 2 000 → error.
	err := checkCapitalAllocation(orch, bots, posUSD(2_000), "")
	if err == nil {
		t.Fatal("expected error for overallocation")
	}
}

func TestCheckCapitalAllocation_USD_ExactBudget_Passes(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryUSD("bot1", 5_000),
	}
	// Exactly 5 000 remaining — requesting exactly 5 000 should pass.
	if err := checkCapitalAllocation(orch, bots, posUSD(5_000), ""); err != nil {
		t.Fatalf("expected nil for exact budget, got %v", err)
	}
}

func TestCheckCapitalAllocation_USD_ExcludesUpdatedBot(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryUSD("bot1", 7_000), // currently allocated 7 000
		botSummaryUSD("bot2", 2_000),
	}
	// Updating bot1 from 7 000 → 8 000.
	// Without exclude: used = 9 000, available = 1 000 → fail.
	// With exclude bot1: used = 2 000, available = 8 000 → pass.
	if err := checkCapitalAllocation(orch, bots, posUSD(8_000), "bot1"); err != nil {
		t.Fatalf("expected nil when excluding updated bot, got %v", err)
	}
}

func TestCheckCapitalAllocation_USD_ExcludedBot_StillExceedsTotal(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryUSD("bot1", 5_000),
		botSummaryUSD("bot2", 4_000),
	}
	// Updating bot1 from 5 000 → 7 000.
	// Exclude bot1: remaining used = 4 000, available = 6 000. 7 000 > 6 000 → error.
	err := checkCapitalAllocation(orch, bots, posUSD(7_000), "bot1")
	if err == nil {
		t.Fatal("expected error when updated allocation still exceeds available")
	}
}

// ── Percentage allocation ─────────────────────────────────────────────────────

func TestCheckCapitalAllocation_Pct_Within100(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryPct("bot1", 0.40),
		botSummaryPct("bot2", 0.30),
	}
	// 70% used, requesting 20% → total 90% → OK.
	if err := checkCapitalAllocation(orch, bots, posPct(0.20), ""); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestCheckCapitalAllocation_Pct_Exceeds100(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryPct("bot1", 0.60),
		botSummaryPct("bot2", 0.30),
	}
	// 90% used, requesting 20% → 110% → error.
	err := checkCapitalAllocation(orch, bots, posPct(0.20), "")
	if err == nil {
		t.Fatal("expected error for >100%% pct allocation")
	}
}

func TestCheckCapitalAllocation_Pct_Exactly100_Passes(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryPct("bot1", 0.50),
	}
	// 50% used, requesting exactly 50% → total 100% → OK.
	if err := checkCapitalAllocation(orch, bots, posPct(0.50), ""); err != nil {
		t.Fatalf("expected nil at exactly 100%%, got %v", err)
	}
}

func TestCheckCapitalAllocation_Pct_ExcludesUpdatedBot(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryPct("bot1", 0.70), // currently 70%
		botSummaryPct("bot2", 0.20),
	}
	// Updating bot1 from 70% → 75%.
	// Without exclude: 90% used, available 10%; 75% > 10% → fail.
	// With exclude bot1: 20% used, available 80%; 75% ≤ 80% → pass.
	if err := checkCapitalAllocation(orch, bots, posPct(0.75), "bot1"); err != nil {
		t.Fatalf("expected nil when excluding updated bot, got %v", err)
	}
}

func TestCheckCapitalAllocation_Pct_ExcludeButStillExceeds(t *testing.T) {
	orch := orchWithCapital(10_000)
	bots := []domain.BotSummary{
		botSummaryPct("bot1", 0.40),
		botSummaryPct("bot2", 0.50),
	}
	// Updating bot1 from 40% → 60%.
	// Exclude bot1: 50% used, available 50%; 60% > 50% → error.
	err := checkCapitalAllocation(orch, bots, posPct(0.60), "bot1")
	if err == nil {
		t.Fatal("expected error when updated pct still exceeds available")
	}
}

// ── Mixed (both dimensions zero on new bot) ───────────────────────────────────

func TestCheckCapitalAllocation_BothZero_NoBots_Passes(t *testing.T) {
	orch := orchWithCapital(10_000)
	if err := checkCapitalAllocation(orch, nil, domain.PositionConfig{}, ""); err != nil {
		t.Fatalf("expected nil for empty allocation request, got %v", err)
	}
}

func TestCheckCapitalAllocation_EmptyExistingBots_USD_Passes(t *testing.T) {
	orch := orchWithCapital(10_000)
	if err := checkCapitalAllocation(orch, []domain.BotSummary{}, posUSD(9_999), ""); err != nil {
		t.Fatalf("expected nil when no other bots exist, got %v", err)
	}
}
