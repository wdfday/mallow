package exchange

import (
	"context"
	"errors"
	"net"
	"strings"
)

// ── Error classification ──────────────────────────────────────────────────────

// ErrClass categorises exchange API errors for metric instrumentation.
type ErrClass int

const (
	ErrClassUnknown             ErrClass = 0
	ErrClassAuth                ErrClass = 1
	ErrClassRateLimit           ErrClass = 2
	ErrClassInsufficientBalance ErrClass = 3
	ErrClassLotSize             ErrClass = 4
	ErrClassPriceFilter         ErrClass = 5
	ErrClassInvalidSymbol       ErrClass = 6
	ErrClassOrderNotFound       ErrClass = 7
	ErrClassNetwork             ErrClass = 8
	ErrClassServerError         ErrClass = 9
	ErrClassClockSkew           ErrClass = 10 // request timestamp outside exchange recvWindow

	ErrClassCount = 11 // array bound; keep in sync with constants above
)

// ErrClassName maps each ErrClass to the label value used in Prometheus metrics.
var ErrClassName = [ErrClassCount]string{
	"unknown", "auth", "rate_limit", "insufficient_balance", "lot_size",
	"price_filter", "invalid_symbol", "order_not_found", "network", "server_error",
	"clock_skew",
}

// ErrorClassifier is an optional interface implemented by exchange adapters to provide
// exchange-specific error classification beyond the generic transport heuristics.
// MeteredExchange checks for this interface and falls back to ClassifyGeneric when absent.
type ErrorClassifier interface {
	ClassifyError(err error) ErrClass
}

// ClassifyGeneric provides transport-level error classification using sentinel checks
// and string heuristics. Adapters should prefer implementing ErrorClassifier with
// native SDK types for accurate per-code classification.
func ClassifyGeneric(err error) ErrClass {
	if err == nil {
		return ErrClassUnknown
	}
	if errors.Is(err, ErrRejected) {
		return ErrClassUnknown
	}
	if IsAmbiguousPlaceError(err) {
		return ErrClassNetwork
	}
	msg := strings.ToLower(err.Error())
	switch {
	// NOTE: bare "signature" is deliberately NOT a keyword here — every signed
	// REST call's URL contains "signature=<hex>" as a query parameter, so a pure
	// transport failure (DNS, dropped connection, timeout) on any signed endpoint
	// would false-positive as auth if we matched on it, since Go's http/url error
	// wrapping includes the full request URL in err.Error(). Use "invalid signature"
	// instead — specific enough to only match a genuine rejection message, and it
	// never occurs as a substring of "...&signature=<hex>...".
	case containsAny(msg, "unauthorized", "invalid api-key", "api key", "invalid signature", "authentication"):
		return ErrClassAuth
	case containsAny(msg, "rate limit", "too many requests", "request too frequent"):
		return ErrClassRateLimit
	case containsAny(msg, "insufficient balance", "insufficient funds", "account has insufficient"):
		return ErrClassInsufficientBalance
	case containsAny(msg, "lot_size", "lot size", "min qty", "minimum quantity", "qty below", "size below"):
		return ErrClassLotSize
	case containsAny(msg, "price_filter", "price filter", "tick size", "price is out of"):
		return ErrClassPriceFilter
	case containsAny(msg, "invalid symbol", "symbol not found", "instrument id does not exist"):
		return ErrClassInvalidSymbol
	case containsAny(msg, "order not found", "order does not exist", "unknown order"):
		return ErrClassOrderNotFound
	case containsAny(msg, "internal server error", "server error"):
		return ErrClassServerError
	default:
		return ErrClassUnknown
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

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
	// Typed detection: ANY DNS resolution failure is a transport failure, regardless
	// of its Timeout()/Temporary() classification — a SERVFAIL ("server misbehaving")
	// is neither a timeout nor "not found", but the request never reached the exchange
	// either way. Confirmed in production: a Binance place-order call that failed to
	// resolve demo-api.binance.com (resolver SERVFAIL) was misclassified as ErrClassAuth
	// by the substring fallback below, because the failed request's URL itself contains
	// "signature=..." (every signed REST call does) — see ClassifyGeneric's comment.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// Substring fallback for SDK errors wrapped via fmt.Errorf (losing the net.Error type).
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{
		"timeout", "deadline", "i/o timeout", "connection reset",
		"connection refused", "eof", "no such host", "tls handshake",
		"temporarily unavailable", "server misbehaving", "503", "502", "504",
	} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}
