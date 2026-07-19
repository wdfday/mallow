package exchange_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"mallow/helm/internal/infra/exchange"
)

type fakeTimeout struct{}

func (fakeTimeout) Error() string   { return "fake" }
func (fakeTimeout) Timeout() bool   { return true }
func (fakeTimeout) Temporary() bool { return true }

func TestIsAmbiguousPlaceError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"sentinel ambiguous", fmt.Errorf("place: %w", exchange.ErrAmbiguous), true},
		{"sentinel rejected wins over substring", fmt.Errorf("timeout but %w", exchange.ErrRejected), false},
		{"context deadline", context.DeadlineExceeded, true},
		{"net.Error timeout", fmt.Errorf("dial: %w", fakeTimeout{}), true},
		{"substring 503", errors.New("binance: HTTP 503 service unavailable"), true},
		{"substring i/o timeout", errors.New("read tcp: i/o timeout"), true},
		{"definitive reject -1013", errors.New("<APIError> code=-1013, msg=Filter failure: LOT_SIZE"), false},
		{"insufficient balance", errors.New("code=-2010 insufficient balance"), false},
		{
			"typed net.DNSError (SERVFAIL, not a timeout)",
			fmt.Errorf("dial: %w", &net.DNSError{Err: "server misbehaving", Name: "demo-api.binance.com"}),
			true,
		},
	}
	for _, c := range cases {
		if got := exchange.IsAmbiguousPlaceError(c.err); got != c.want {
			t.Errorf("%s: IsAmbiguousPlaceError = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestClassifyGeneric_DNSFailureOnSignedRequest is a regression test for a bug
// observed in production (2026-07-16): a Binance place-order call that failed
// DNS resolution (resolver SERVFAIL — "server misbehaving") was classified as
// ErrClassAuth instead of ErrClassNetwork, because the failed request's own
// URL contains "signature=<hex>" (every signed REST call's URL does), and the
// old auth-detection keyword list matched the bare substring "signature"
// anywhere in the error — including inside the URL of a request that never
// reached the exchange. This caused an immediate incorrect pause + "credential
// error" broker-connection mark for what was actually a transient DNS blip
// (which should tolerate errClassThresholds[ErrClassNetwork]=5 consecutive
// failures before escalating, framed as a network issue, not credentials).
func TestClassifyGeneric_DNSFailureOnSignedRequest(t *testing.T) {
	// Mirrors the real error shape: *url.Error wraps the dial failure and
	// prepends "Post \"<url-with-signature-param>\": ".
	err := &net.DNSError{Err: "server misbehaving", Name: "demo-api.binance.com"}
	wrapped := fmt.Errorf(
		"binance spot place order: Post %q: dial tcp: lookup demo-api.binance.com: %w",
		"https://demo-api.binance.com/api/v3/order?timestamp=1784178479892&signature=6555dd0ed11b14df5b3d95897daea416828df8641dc762e4d3768f8706ee4fb3",
		err,
	)

	if got := exchange.ClassifyGeneric(wrapped); got != exchange.ErrClassNetwork {
		t.Fatalf("ClassifyGeneric(DNS failure on signed URL) = %s, want %s",
			exchange.ErrClassName[got], exchange.ErrClassName[exchange.ErrClassNetwork])
	}
}

// TestClassifyGeneric_GenuineAuthRejectionStillDetected guards against fixing
// the false positive above by breaking real auth detection — a genuine
// rejection message using the word "signature" (e.g. "invalid signature")
// must still classify as ErrClassAuth.
func TestClassifyGeneric_GenuineAuthRejectionStillDetected(t *testing.T) {
	cases := []string{
		"unauthorized: invalid api-key",
		"authentication failed",
		"rejected: invalid signature for this request",
	}
	for _, msg := range cases {
		if got := exchange.ClassifyGeneric(errors.New(msg)); got != exchange.ErrClassAuth {
			t.Errorf("ClassifyGeneric(%q) = %s, want auth", msg, exchange.ErrClassName[got])
		}
	}
}
