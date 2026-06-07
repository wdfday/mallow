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
