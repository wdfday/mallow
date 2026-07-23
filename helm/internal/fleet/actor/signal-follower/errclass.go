package signalfollower

import "mallow/helm/internal/infra/exchange"

// ErrClassThresholds is the number of consecutive PlaceOrder failures of a
// given exchange.ErrClass required before it escalates to OnAccountError.
// A zero threshold means the class never escalates — it stays metrics-only
// (exchange.ClassifyGeneric/ErrorClassifier still tag it for Prometheus),
// same as before this generalization for every class except Auth.
var ErrClassThresholds = [exchange.ErrClassCount]int32{
	exchange.ErrClassAuth:        1, // credentials rarely fail transiently
	exchange.ErrClassNetwork:     5, // a couple of dropped connections is normal
	exchange.ErrClassServerError: 5, // exchange-side 5xx/maintenance — same reasoning
	// All other classes (RateLimit, ClockSkew, InsufficientBalance, LotSize,
	// PriceFilter, InvalidSymbol, OrderNotFound, Unknown) default to 0 —
	// either self-resolving (rate limit backoff, clock sync) or per-order
	// validation issues that don't belong at the account/connection level.
}

// ClassifyErr uses the exchange adapter's precise ErrorClassifier when the
// wrapped exchange implements it (MeteredExchange forwards this from the
// inner adapter — see infra/exchange/metrics.go), falling back to the
// transport-only string heuristic otherwise. Adapter-specific typed codes
// (Binance/fbinance API codes, OKX sCodes, Bybit retCodes, Alpaca status
// codes) are far more precise than ClassifyGeneric's substring matching —
// see errors_test.go's DNS-vs-signature regression for why that matters.
func ClassifyErr(ex exchange.Exchange, err error) exchange.ErrClass {
	if c, ok := ex.(exchange.ErrorClassifier); ok {
		return c.ClassifyError(err)
	}
	return exchange.ClassifyGeneric(err)
}
