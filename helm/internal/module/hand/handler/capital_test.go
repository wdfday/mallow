package handler

// checkCapitalAllocation is an unexported package-level function, so tests
// live in the same package (white-box, no _test suffix on package name).

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	handdomain "mallow/helm/internal/module/hand/domain"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// botID returns a deterministic UUID for a test name string.
func botID(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte(name))
}

func botSummaryUSD(id uuid.UUID, allocated float64) handdomain.HandSummary {
	return handdomain.HandSummary{
		ID:               id,
		AllocatedCapital: decimal.NewFromFloat(allocated),
	}
}

func posUSD(v float64) decimal.Decimal {
	return decimal.NewFromFloat(v)
}

// ── No allocation requested — always passes ───────────────────────────────────

func TestCheckCapitalAllocation_NoAllocation_Passes(t *testing.T) {
	bots := []handdomain.HandSummary{
		botSummaryUSD(botID("bot1"), 5_000),
	}
	if overflow, _ := checkCapitalAllocation(10_000, bots, decimal.Zero, ""); overflow != nil {
		t.Fatalf("expected nil, got %v", overflow.Error)
	}
}

// ── Fixed-USD allocation ─────────────────────────────────────────────────────

func TestCheckCapitalAllocation_USD_WithinBudget(t *testing.T) {
	bots := []handdomain.HandSummary{
		botSummaryUSD(botID("bot1"), 3_000),
		botSummaryUSD(botID("bot2"), 3_000),
	}
	// 4 000 requested, 4 000 available → OK.
	if overflow, _ := checkCapitalAllocation(10_000, bots, posUSD(4_000), ""); overflow != nil {
		t.Fatalf("expected nil, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_USD_ExceedsBudget(t *testing.T) {
	bots := []handdomain.HandSummary{
		botSummaryUSD(botID("bot1"), 6_000),
		botSummaryUSD(botID("bot2"), 3_000),
	}
	// 9 000 used, only 1 000 left; requesting 2 000 → overflow.
	overflow, _ := checkCapitalAllocation(10_000, bots, posUSD(2_000), "")
	if overflow == nil {
		t.Fatal("expected overflow for overallocation")
	}
}

func TestCheckCapitalAllocation_USD_ExceedsBudget_HasSuggestions(t *testing.T) {
	bots := []handdomain.HandSummary{
		botSummaryUSD(botID("bot1"), 6_000), // deployed=0, reducible=6000
		botSummaryUSD(botID("bot2"), 3_000), // deployed=0, reducible=3000
	}
	overflow, _ := checkCapitalAllocation(10_000, bots, posUSD(2_000), "")
	if overflow == nil {
		t.Fatal("expected overflow")
	}
	if len(overflow.Suggestions) == 0 {
		t.Fatal("expected suggestions when hands have free capital")
	}
}

func TestCheckCapitalAllocation_USD_ExceedsBudget_NoSuggestions_WhenFullyDeployed(t *testing.T) {
	b1 := botSummaryUSD(botID("bot1"), 6_000)
	b1.DeployedCapital = decimal.NewFromFloat(6_000) // fully deployed, can't reduce
	bots := []handdomain.HandSummary{b1, botSummaryUSD(botID("bot2"), 3_000)}
	overflow, _ := checkCapitalAllocation(10_000, bots, posUSD(2_000), "")
	if overflow == nil {
		t.Fatal("expected overflow")
	}
	// bot1 is fully deployed → not in suggestions; bot2 has 3000 free → in suggestions
	for _, s := range overflow.Suggestions {
		if s.HandID == botID("bot1").String() {
			t.Fatal("fully-deployed bot1 should not appear in suggestions")
		}
	}
}

func TestCheckCapitalAllocation_USD_ExactBudget_Passes(t *testing.T) {
	bots := []handdomain.HandSummary{
		botSummaryUSD(botID("bot1"), 5_000),
	}
	// Exactly 5 000 remaining — requesting exactly 5 000 should pass.
	if overflow, _ := checkCapitalAllocation(10_000, bots, posUSD(5_000), ""); overflow != nil {
		t.Fatalf("expected nil for exact budget, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_USD_ExcludesUpdatedBot(t *testing.T) {
	bots := []handdomain.HandSummary{
		botSummaryUSD(botID("bot1"), 7_000), // currently allocated 7 000
		botSummaryUSD(botID("bot2"), 2_000),
	}
	// Updating bot1 from 7 000 → 8 000.
	// Without exclude: used = 9 000, available = 1 000 → fail.
	// With exclude bot1: used = 2 000, available = 8 000 → pass.
	if overflow, _ := checkCapitalAllocation(10_000, bots, posUSD(8_000), botID("bot1").String()); overflow != nil {
		t.Fatalf("expected nil when excluding updated bot, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_USD_ExcludedBot_StillExceedsTotal(t *testing.T) {
	bots := []handdomain.HandSummary{
		botSummaryUSD(botID("bot1"), 5_000),
		botSummaryUSD(botID("bot2"), 4_000),
	}
	// Updating bot1 from 5 000 → 7 000.
	// Exclude bot1: remaining used = 4 000, available = 6 000. 7 000 > 6 000 → overflow.
	overflow, _ := checkCapitalAllocation(10_000, bots, posUSD(7_000), botID("bot1").String())
	if overflow == nil {
		t.Fatal("expected overflow when updated allocation still exceeds available")
	}
}

// ── No allocation (zero) ──────────────────────────────────────────────────────

func TestCheckCapitalAllocation_ZeroAlloc_NoBots_Passes(t *testing.T) {
	if overflow, _ := checkCapitalAllocation(10_000, nil, decimal.Zero, ""); overflow != nil {
		t.Fatalf("expected nil for empty allocation request, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_EmptyExistingBots_USD_Passes(t *testing.T) {
	if overflow, _ := checkCapitalAllocation(10_000, []handdomain.HandSummary{}, posUSD(9_999), ""); overflow != nil {
		t.Fatalf("expected nil when no other hands exist, got %v", overflow.Error)
	}
}
