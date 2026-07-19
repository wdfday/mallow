package act

// types.go — Alpaca SDK native types and converters to internal exchange types.
//
// Layout:
//   Error classifier — alpacasdk.APIError → exchange.ErrClass
//   Order converter  — alpacasdk.Order → exchange.OrderResult

import (
	"errors"
	"net/http"
	"strings"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"

	"mallow/helm/internal/infra/exchange"
)

// ── Error classifier ──────────────────────────────────────────────────────────

// ClassifyError implements exchange.ErrorClassifier using Alpaca's HTTP status
// codes. Alpaca has no documented numeric error-code taxonomy like Binance/OKX/
// Bybit — status code is the only reliable convention — except HTTP 403, which
// Alpaca reuses for both genuine permission denials AND business-rule rejects
// (insufficient buying power, PDT protection, short restriction), so that one
// bucket needs a message-substring fallback to split apart.
// Reference: https://alpaca.markets/learn/how-to-fix-common-trading-api-errors-at-alpaca
func (c *Client) ClassifyError(err error) exchange.ErrClass {
	var apiErr *alpacasdk.APIError
	if !errors.As(err, &apiErr) {
		return exchange.ClassifyGeneric(err)
	}
	switch apiErr.StatusCode {
	case http.StatusUnauthorized: // 401 — bad/missing API key
		return exchange.ErrClassAuth
	case http.StatusForbidden: // 403 — shared bucket, split by message
		msg := strings.ToLower(apiErr.Message)
		if strings.Contains(msg, "insufficient buying power") || strings.Contains(msg, "insufficient qty available") {
			return exchange.ErrClassInsufficientBalance
		}
		// "insufficient permission", "subscription does not permit", "not
		// authorized to trade", "restricted to liquidation only", "not allowed
		// to short", "pattern day trading protection", "asset is not
		// fractionable" — all account/permission-level denials, same bucket as
		// a genuine 401.
		return exchange.ErrClassAuth
	case http.StatusNotFound: // 404 — GetOrder/CancelOrder on an unknown order id
		return exchange.ErrClassOrderNotFound
	case http.StatusTooManyRequests: // 429 — 200 req/min per account
		return exchange.ErrClassRateLimit
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout: // 502/503/504
		return exchange.ErrClassNetwork
	case http.StatusInternalServerError: // 500
		return exchange.ErrClassServerError
	default:
		// 422 covers order-shape validation (invalid time_in_force, missing
		// stop price, duplicate client_order_id, etc.) — request-level mistakes,
		// not one of the existing ErrClass buckets. Left to ClassifyGeneric
		// rather than forced into a category that doesn't fit.
		return exchange.ClassifyGeneric(err)
	}
}

// ── Order converters: Alpaca SDK → exchange.OrderResult ──────────────────────

// mapOrder converts an Alpaca SDK Order to the internal OrderResult.
// Alpaca order IDs are plain UUIDs — no compound encoding needed.
func mapOrder(o *alpacasdk.Order) *exchange.OrderResult {
	result := &exchange.OrderResult{
		ID:            o.ID,
		ClientOrderID: o.ClientOrderID,
		Symbol:        o.Symbol,
		Side:          exchange.OrderSide(o.Side),
		Status:        o.Status,
		FilledQty:     o.FilledQty,
	}
	if o.FilledAvgPrice != nil {
		result.FilledAvg = *o.FilledAvgPrice
	}
	if o.Qty != nil {
		result.Qty = *o.Qty
	}
	return result
}
