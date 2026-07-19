package clid_test

import (
	"strings"
	"testing"

	"mallow/helm/internal/fleet/actor/clid"
)

func TestClid_GeneratedIsRecognisedAsOurs(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := clid.New()

		if !clid.IsOurClid(id) {
			t.Fatalf("clid.New produced %q which clid.IsOurClid rejects — format/detection drift", id)
		}
		if !strings.HasPrefix(id, clid.Prefix) {
			t.Fatalf("clid %q lacks prefix %q", id, clid.Prefix)
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
	id := clid.New()
	const exID = "BTCUSDT:12345"

	// Our order: clid wins over the exchange id.
	if got := clid.CanonKey(id, exID); got != id {
		t.Errorf("clid.CanonKey(ours, ex) = %q, want clid %q", got, id)
	}
	// Foreign/bracket order (no mlw prefix): falls back to the exchange id.
	if got := clid.CanonKey("x-binance-auto-123", exID); got != exID {
		t.Errorf("clid.CanonKey(foreign, ex) = %q, want exchange id %q", got, exID)
	}
	// Empty clid (adapter didn't echo): falls back to the exchange id.
	if got := clid.CanonKey("", exID); got != exID {
		t.Errorf("clid.CanonKey(empty, ex) = %q, want exchange id %q", got, exID)
	}
}
