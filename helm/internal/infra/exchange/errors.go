package exchange

import (
	"context"
	"errors"
	"net"
	"strings"
)

// Order-placement error classification lives here, in the transport layer that owns
// exchange/SDK error shapes — not in the runtime, which should not be parsing adapter
// error strings. Adapters MAY wrap their errors with ErrAmbiguous / ErrRejected for a
// precise, type-based classification; IsAmbiguousPlaceError falls back to a transport
// heuristic (net.Error timeout, context deadline, common substrings) for adapters that
// don't. See CLIENT_ORDER_ID.md.
var (
	// ErrAmbiguous marks a PlaceOrder failure whose outcome is unknown — the order may or
	// may not have reached the exchange (timeout, dropped connection). Callers must NOT
	// assume the order failed; query by clid to confirm. Wrap with fmt.Errorf("...: %w", ...).
	ErrAmbiguous = errors.New("exchange: order placement outcome ambiguous")

	// ErrRejected marks a PlaceOrder failure the exchange definitively rejected — the order
	// certainly did not land (insufficient balance, lot size, validation). Safe to abandon.
	ErrRejected = errors.New("exchange: order definitively rejected")
)

// IsAmbiguousPlaceError reports whether a PlaceOrder error leaves the order's fate unknown
// (transport failure) versus a definitive reject. Checks the typed sentinels first, then a
// transport heuristic for unwrapped SDK errors.
func IsAmbiguousPlaceError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRejected) {
		return false
	}
	if errors.Is(err, ErrAmbiguous) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	// Typed detection: a net.Error timeout is unambiguously a transport failure.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	// Substring fallback for SDK errors wrapped via fmt.Errorf (losing the net.Error type).
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{
		"timeout", "deadline", "i/o timeout", "connection reset",
		"connection refused", "eof", "no such host", "tls handshake",
		"temporarily unavailable", "503", "502", "504",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}
