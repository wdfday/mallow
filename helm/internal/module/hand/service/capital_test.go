package service_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
	handservice "mallow/helm/internal/module/hand/service"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func botID(name string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte(name))
}

func botSummaryUSD(id uuid.UUID, allocated float64) domain.HandSummary {
	return domain.HandSummary{
		ID:               id,
		AllocatedCapital: decimal.NewFromFloat(allocated),
	}
}

func posUSD(v float64) decimal.Decimal {
	return decimal.NewFromFloat(v)
}

// ── CheckCapitalAllocation ────────────────────────────────────────────────────

func TestCheckCapitalAllocation_NoAllocation_Passes(t *testing.T) {
	bots := []domain.HandSummary{botSummaryUSD(botID("bot1"), 5_000)}
	if overflow, _ := handservice.CheckCapitalAllocation(10_000, bots, decimal.Zero, ""); overflow != nil {
		t.Fatalf("expected nil, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_USD_WithinBudget(t *testing.T) {
	bots := []domain.HandSummary{
		botSummaryUSD(botID("bot1"), 3_000),
		botSummaryUSD(botID("bot2"), 3_000),
	}
	if overflow, _ := handservice.CheckCapitalAllocation(10_000, bots, posUSD(4_000), ""); overflow != nil {
		t.Fatalf("expected nil, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_USD_ExceedsBudget(t *testing.T) {
	bots := []domain.HandSummary{
		botSummaryUSD(botID("bot1"), 6_000),
		botSummaryUSD(botID("bot2"), 3_000),
	}
	overflow, _ := handservice.CheckCapitalAllocation(10_000, bots, posUSD(2_000), "")
	if overflow == nil {
		t.Fatal("expected overflow for overallocation")
	}
}

func TestCheckCapitalAllocation_USD_ExceedsBudget_HasSuggestions(t *testing.T) {
	bots := []domain.HandSummary{
		botSummaryUSD(botID("bot1"), 6_000),
		botSummaryUSD(botID("bot2"), 3_000),
	}
	overflow, _ := handservice.CheckCapitalAllocation(10_000, bots, posUSD(2_000), "")
	if overflow == nil {
		t.Fatal("expected overflow")
	}
	if len(overflow.Suggestions) == 0 {
		t.Fatal("expected suggestions when hands have free capital")
	}
}

func TestCheckCapitalAllocation_USD_ExceedsBudget_NoSuggestions_WhenFullyDeployed(t *testing.T) {
	b1 := botSummaryUSD(botID("bot1"), 6_000)
	b1.DeployedCapital = decimal.NewFromFloat(6_000)
	bots := []domain.HandSummary{b1, botSummaryUSD(botID("bot2"), 3_000)}
	overflow, _ := handservice.CheckCapitalAllocation(10_000, bots, posUSD(2_000), "")
	if overflow == nil {
		t.Fatal("expected overflow")
	}
	for _, s := range overflow.Suggestions {
		if s.HandID == botID("bot1").String() {
			t.Fatal("fully-deployed bot1 should not appear in suggestions")
		}
	}
}

func TestCheckCapitalAllocation_USD_ExactBudget_Passes(t *testing.T) {
	bots := []domain.HandSummary{botSummaryUSD(botID("bot1"), 5_000)}
	if overflow, _ := handservice.CheckCapitalAllocation(10_000, bots, posUSD(5_000), ""); overflow != nil {
		t.Fatalf("expected nil for exact budget, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_USD_ExcludesUpdatedBot(t *testing.T) {
	bots := []domain.HandSummary{
		botSummaryUSD(botID("bot1"), 7_000),
		botSummaryUSD(botID("bot2"), 2_000),
	}
	if overflow, _ := handservice.CheckCapitalAllocation(10_000, bots, posUSD(8_000), botID("bot1").String()); overflow != nil {
		t.Fatalf("expected nil when excluding updated bot, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_USD_ExcludedBot_StillExceedsTotal(t *testing.T) {
	bots := []domain.HandSummary{
		botSummaryUSD(botID("bot1"), 5_000),
		botSummaryUSD(botID("bot2"), 4_000),
	}
	overflow, _ := handservice.CheckCapitalAllocation(10_000, bots, posUSD(7_000), botID("bot1").String())
	if overflow == nil {
		t.Fatal("expected overflow when updated allocation still exceeds available")
	}
}

func TestCheckCapitalAllocation_ZeroAlloc_NoBots_Passes(t *testing.T) {
	if overflow, _ := handservice.CheckCapitalAllocation(10_000, nil, decimal.Zero, ""); overflow != nil {
		t.Fatalf("expected nil for empty allocation request, got %v", overflow.Error)
	}
}

func TestCheckCapitalAllocation_EmptyExistingBots_USD_Passes(t *testing.T) {
	if overflow, _ := handservice.CheckCapitalAllocation(10_000, []domain.HandSummary{}, posUSD(9_999), ""); overflow != nil {
		t.Fatalf("expected nil when no other hands exist, got %v", overflow.Error)
	}
}

// ── CheckSymbolConflict ───────────────────────────────────────────────────────

func handWithSymbols(id uuid.UUID, name string, status domain.HandStatus, symbols ...string) domain.HandSummary {
	return domain.HandSummary{ID: id, Name: name, Status: status, Symbols: symbols}
}

func TestCheckSymbolConflict_FreeSymbol_Passes(t *testing.T) {
	existing := []domain.HandSummary{
		handWithSymbols(botID("a"), "alpha", domain.HandStatusRunning, "BTCUSDT"),
	}
	if err := handservice.CheckSymbolConflict(existing, []string{"ETHUSDT"}, ""); err != nil {
		t.Fatalf("distinct symbol should pass, got: %v", err)
	}
}

func TestCheckSymbolConflict_TakenSymbol_Blocks(t *testing.T) {
	existing := []domain.HandSummary{
		handWithSymbols(botID("a"), "alpha", domain.HandStatusRunning, "BTCUSDT"),
	}
	if err := handservice.CheckSymbolConflict(existing, []string{"BTCUSDT"}, ""); err == nil {
		t.Fatal("expected conflict when another hand already trades the symbol")
	}
}

func TestCheckSymbolConflict_TerminalHand_Ignored(t *testing.T) {
	existing := []domain.HandSummary{
		handWithSymbols(botID("a"), "alpha", domain.HandStatusKilled, "BTCUSDT"),
		handWithSymbols(botID("b"), "beta", domain.HandStatusReleased, "BTCUSDT"),
	}
	if err := handservice.CheckSymbolConflict(existing, []string{"BTCUSDT"}, ""); err != nil {
		t.Fatalf("terminal (killed/released) hands must not block the symbol, got: %v", err)
	}
}

func TestCheckSymbolConflict_ExcludesSelf(t *testing.T) {
	self := botID("a")
	existing := []domain.HandSummary{
		handWithSymbols(self, "alpha", domain.HandStatusRunning, "BTCUSDT"),
	}
	if err := handservice.CheckSymbolConflict(existing, []string{"BTCUSDT"}, self.String()); err != nil {
		t.Fatalf("a hand should not conflict with itself, got: %v", err)
	}
}
