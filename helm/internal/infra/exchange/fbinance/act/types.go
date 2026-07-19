package act

// types.go — Binance USDM futures native types and converters to internal exchange types.

import (
	"errors"
	"strconv"
	"strings"

	"github.com/adshao/go-binance/v2/common"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// isBinanceCode returns true when err is a Binance API error with the given code.
func isBinanceCode(err error, code int64) bool {
	var apiErr *common.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == code
	}
	return false
}

// ClassifyError implements exchange.ErrorClassifier using Binance USDⓈ-M
// futures API error codes. Shares the -10xx/-20xx range with binance spot
// (same underlying go-binance/v2 SDK, same common.APIError type) plus a
// futures-specific -40xx margin/position range.
// Reference: https://developers.binance.com/docs/derivatives/usds-margined-futures/error-code
func (c *Client) ClassifyError(err error) exchange.ErrClass {
	var apiErr *common.APIError
	if !errors.As(err, &apiErr) {
		return exchange.ClassifyGeneric(err)
	}
	switch apiErr.Code {
	case -2014, -2015, -1002, -1022: // API-key format invalid, invalid key/IP/permissions, unauthorized, bad signature
		return exchange.ErrClassAuth
	case -1003, -1015, -2025: // too many requests, too many new orders, max open order limit reached
		return exchange.ErrClassRateLimit
	case -2018, -2019, -4051, -4050: // balance insufficient, margin insufficient, isolated/cross balance insufficient
		return exchange.ErrClassInsufficientBalance
	case -1121: // invalid symbol
		return exchange.ErrClassInvalidSymbol
	case -1111, -4004, -4005: // precision over max, qty below min, qty above max
		return exchange.ErrClassLotSize
	case -2013: // order does not exist
		return exchange.ErrClassOrderNotFound
	case -1021: // timestamp outside recvWindow — local clock drift
		return exchange.ErrClassClockSkew
	case -1001, -1006, -1007: // internal disconnect, unexpected response, timeout waiting for response
		return exchange.ErrClassNetwork
	case -1000, -1008, -1016: // unknown server error, server overloaded, service shutting down
		return exchange.ErrClassServerError
	default:
		// -2023 (in liquidation mode), -2024 (position not sufficient), -4028
		// (invalid leverage) and other position/leverage-specific codes don't
		// map cleanly onto any existing ErrClass — left as Unknown (metrics-only)
		// rather than forced into a category that doesn't fit.
		return exchange.ClassifyGeneric(err)
	}
}

// ── Parse helpers ─────────────────────────────────────────────────────────────

func parseDecimal(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// futuresCreateToResult maps a futures CreateOrderResponse to OrderResult.
func futuresCreateToResult(side exchange.OrderSide, r *futures.CreateOrderResponse) *exchange.OrderResult {
	return &exchange.OrderResult{
		ID:            strconv.FormatInt(r.OrderID, 10),
		ClientOrderID: r.ClientOrderID,
		Symbol:        r.Symbol,
		Side:          side,
		Status:        strings.ToLower(string(r.Status)),
		Qty:           parseDecimal(r.OrigQuantity),
		FilledQty:     parseDecimal(r.ExecutedQuantity),
	}
}

// binanceFuturesTIF maps the canonical TIF to Binance futures TimeInForce. Default: GTC.
func binanceFuturesTIF(tif exchange.TimeInForce) futures.TimeInForceType {
	switch tif {
	case exchange.TIFIOC:
		return futures.TimeInForceTypeIOC
	case exchange.TIFFOK:
		return futures.TimeInForceTypeFOK
	default:
		return futures.TimeInForceTypeGTC
	}
}

// ensure parseFloat is used (suppress lint if needed)
var _ = parseFloat
