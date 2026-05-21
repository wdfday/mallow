package exchange_test

import (
	"testing"
	"time"

	"mallow/helm/internal/infra/exchange"
)

func TestBackoff_CalculatesNextDuration(t *testing.T) {
	b := exchange.Backoff{
		Min:    1 * time.Second,
		Max:    10 * time.Second,
		Factor: 2.0,
		Jitter: false,
	}

	// Attempt 0: Min
	d := b.Next(0)
	if d != 1*time.Second {
		t.Fatalf("expected 1s, got %s", d)
	}

	// Attempt 1: Min * 2
	d = b.Next(1)
	if d != 2*time.Second {
		t.Fatalf("expected 2s, got %s", d)
	}

	// Attempt 2: Min * 4
	d = b.Next(2)
	if d != 4*time.Second {
		t.Fatalf("expected 4s, got %s", d)
	}

	// Attempt 3: Min * 8
	d = b.Next(3)
	if d != 8*time.Second {
		t.Fatalf("expected 8s, got %s", d)
	}

	// Attempt 4: Capped at Max
	d = b.Next(4)
	if d != 10*time.Second {
		t.Fatalf("expected 10s, got %s", d)
	}
}

func TestBackoff_WithJitter(t *testing.T) {
	b := exchange.Backoff{
		Min:    1 * time.Second,
		Max:    10 * time.Second,
		Factor: 2.0,
		Jitter: true,
	}

	for i := 0; i < 50; i++ {
		d := b.Next(2) // Base should be 4s
		// with jitter, it should be between 3.6s and 4.4s (10% range)
		if d < 3500*time.Millisecond || d > 4500*time.Millisecond {
			t.Fatalf("expected jittered value around 4s, got %s", d)
		}
	}
}
