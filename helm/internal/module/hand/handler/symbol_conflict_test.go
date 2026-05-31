package handler

import (
	"testing"

	"github.com/google/uuid"

	handdomain "mallow/helm/internal/module/hand/domain"
)

func handWithSymbols(id uuid.UUID, name string, status handdomain.HandStatus, symbols ...string) handdomain.HandSummary {
	return handdomain.HandSummary{ID: id, Name: name, Status: status, Symbols: symbols}
}

func TestCheckSymbolConflict_FreeSymbol_Passes(t *testing.T) {
	existing := []handdomain.HandSummary{
		handWithSymbols(botID("a"), "alpha", handdomain.HandStatusRunning, "BTCUSDT"),
	}
	if err := checkSymbolConflict(existing, []string{"ETHUSDT"}, ""); err != nil {
		t.Fatalf("distinct symbol should pass, got: %v", err)
	}
}

func TestCheckSymbolConflict_TakenSymbol_Blocks(t *testing.T) {
	existing := []handdomain.HandSummary{
		handWithSymbols(botID("a"), "alpha", handdomain.HandStatusRunning, "BTCUSDT"),
	}
	if err := checkSymbolConflict(existing, []string{"BTCUSDT"}, ""); err == nil {
		t.Fatal("expected conflict when another hand already trades the symbol")
	}
}

func TestCheckSymbolConflict_TerminalHand_Ignored(t *testing.T) {
	existing := []handdomain.HandSummary{
		handWithSymbols(botID("a"), "alpha", handdomain.HandStatusKilled, "BTCUSDT"),
		handWithSymbols(botID("b"), "beta", handdomain.HandStatusReleased, "BTCUSDT"),
	}
	if err := checkSymbolConflict(existing, []string{"BTCUSDT"}, ""); err != nil {
		t.Fatalf("terminal (killed/released) hands must not block the symbol, got: %v", err)
	}
}

func TestCheckSymbolConflict_ExcludesSelf(t *testing.T) {
	self := botID("a")
	existing := []handdomain.HandSummary{
		handWithSymbols(self, "alpha", handdomain.HandStatusRunning, "BTCUSDT"),
	}
	// Updating the same hand: its own symbol must not conflict with itself.
	if err := checkSymbolConflict(existing, []string{"BTCUSDT"}, self.String()); err != nil {
		t.Fatalf("a hand should not conflict with itself, got: %v", err)
	}
}
