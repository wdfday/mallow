package runtime

// Internal tests for the clid format ↔ detection invariant.
//
// canonOrderKey/isOurClid identify mallow-generated client order ids by the "mlw" prefix.
// That heuristic is only safe while newClientOrderID keeps producing prefixed ids — if the
// format ever drifts, routing would silently fall back to exchange-id everywhere. These
// tests make such a drift a loud failure instead.
//
// Note on collisions: a *manual* order whose clientOrderId happens to start with "mlw" is
// harmless — canonOrderKey returns that string, PendingOrderHandID finds no tracking entry
// for it, and the fill takes the orphan path (the intended behaviour for manual orders).
// Only a full collision with a live tracked clid could misroute, which the nanosecond +
// random suffix makes negligible.
//
// go test ./internal/runtime/ -run TestClid -count=1

import (
	"strings"
	"testing"
)

func TestClid_GeneratedIsRecognisedAsOurs(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := newClientOrderID()

		if !isOurClid(id) {
			t.Fatalf("newClientOrderID produced %q which isOurClid rejects — format/detection drift", id)
		}
		if !strings.HasPrefix(id, clidPrefix) {
			t.Fatalf("clid %q lacks prefix %q", id, clidPrefix)
		}
		// OKX has the tightest constraint: ≤32 chars, alphanumeric only.
		if len(id) > 32 {
			t.Fatalf("clid %q is %d chars — exceeds the 32-char venue limit", id, len(id))
		}
		for _, r := range id {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				t.Fatalf("clid %q contains non-alphanumeric %q — violates OKX clOrdId charset", id, r)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("clid %q generated twice in 1000 iterations — insufficient entropy", id)
		}
		seen[id] = struct{}{}
	}
}

func TestClid_CanonOrderKeyPrefersOurClid(t *testing.T) {
	clid := newClientOrderID()
	const exID = "BTCUSDT:12345"

	// Our order: clid wins over the exchange id.
	if got := canonOrderKey(clid, exID); got != clid {
		t.Errorf("canonOrderKey(ours, ex) = %q, want clid %q", got, clid)
	}
	// Foreign/bracket order (no mlw prefix): falls back to the exchange id.
	if got := canonOrderKey("x-binance-auto-123", exID); got != exID {
		t.Errorf("canonOrderKey(foreign, ex) = %q, want exchange id %q", got, exID)
	}
	// Empty clid (adapter didn't echo): falls back to the exchange id.
	if got := canonOrderKey("", exID); got != exID {
		t.Errorf("canonOrderKey(empty, ex) = %q, want exchange id %q", got, exID)
	}
}
