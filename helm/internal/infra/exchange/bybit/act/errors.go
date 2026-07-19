package act

import (
	"errors"
	"fmt"

	"mallow/helm/internal/infra/exchange"
)

// bybitAPIError carries a Bybit V5 retCode/retMsg pair, letting ClassifyError
// (types.go) classify precisely via errors.As instead of falling back to
// ClassifyGeneric's fragile string heuristics. Every REST call site that
// checks `resp.RetCode != 0` wraps its error via bybitErr instead of a bare
// fmt.Errorf, so no call path silently loses its retCode.
type bybitAPIError struct {
	Op      string // short call-site label, e.g. "place order", "wallet balance"
	RetCode int
	RetMsg  string
}

func (e *bybitAPIError) Error() string {
	return fmt.Sprintf("bybit %s: retCode=%d retMsg=%s", e.Op, e.RetCode, e.RetMsg)
}

// bybitErr wraps a non-zero Bybit retCode/retMsg into a classifiable error.
func bybitErr(op string, retCode int, retMsg string) error {
	return &bybitAPIError{Op: op, RetCode: retCode, RetMsg: retMsg}
}

var _ exchange.ErrorClassifier = (*Client)(nil)

// ClassifyError implements exchange.ErrorClassifier using Bybit V5 retCodes.
// Reference: https://bybit-exchange.github.io/docs/v5/error
func (c *Client) ClassifyError(err error) exchange.ErrClass {
	var apiErr *bybitAPIError
	if !errors.As(err, &apiErr) {
		return exchange.ClassifyGeneric(err)
	}
	switch apiErr.RetCode {
	case 10003, 10004, 10005, 10007, 10010, 33004, 2015, 401: // invalid API key, bad signature, permission denied,
		// auth failed, unmatched IP, derivatives/spot key expired, incorrect key
		return exchange.ErrClassAuth
	case 10006, 10018, 429, 10429, 20003, 403: // too many visits, IP rate limit, frequency protection, forbidden/IP rate limit
		return exchange.ErrClassRateLimit
	case 110004, 110007, 110012, 110014, 110044, 110045, 170131: // wallet/available balance or margin insufficient
		return exchange.ErrClassInsufficientBalance
	case 10029, 170121: // invalid/unwhitelisted symbol
		return exchange.ErrClassInvalidSymbol
	case 170136, 170381, 110017: // qty below min, qty too large, qty truncated to zero
		return exchange.ErrClassLotSize
	case 110001, 110008, 110010, 170143: // order does not exist, already completed/cancelled, not found on book
		return exchange.ErrClassOrderNotFound
	case 10002: // request time exceeds the time window range — local clock drift
		return exchange.ErrClassClockSkew
	case 10000, 10016, 10017, 10019, 500: // server timeout, internal server error, route not found, service restarting
		return exchange.ErrClassServerError
	default:
		// Position/risk-limit codes (110005, 110006, 110011, 110016, 110020,
		// 110021, 110064, 110067) and generic validation codes (10001, 10014,
		// 20006, 110032) don't map cleanly onto any existing ErrClass — left
		// as Unknown (metrics-only) rather than forced into a wrong category.
		return exchange.ClassifyGeneric(err)
	}
}
