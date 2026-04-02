package validation

import (
	"errors"
	"testing"
)

func TestRequiredTrimmed(t *testing.T) {
	v, err := RequiredTrimmed("name", "  alice  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "alice" {
		t.Fatalf("got %q, want %q", v, "alice")
	}

	_, err = RequiredTrimmed("name", "  ")
	if !errors.Is(err, ErrRequired) {
		t.Fatalf("expected ErrRequired, got %v", err)
	}
}

func TestIsUUID(t *testing.T) {
	if !IsUUID("550e8400-e29b-41d4-a716-446655440000") {
		t.Fatal("expected valid UUID")
	}
	if IsUUID("not-a-uuid") {
		t.Fatal("expected invalid UUID")
	}
}

func TestNormalizeSymbol(t *testing.T) {
	got := NormalizeSymbol("  btcusdt ")
	if got != "BTCUSDT" {
		t.Fatalf("got %q, want %q", got, "BTCUSDT")
	}
}
