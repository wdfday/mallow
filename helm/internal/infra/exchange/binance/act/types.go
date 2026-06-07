package act

// types.go — Binance spot native types and converters to internal exchange types.
//
// Layout:
//   Parse helpers     — string → decimal / float64
//   Side mapping      — gobinance.SideType ↔ exchange.OrderSide
//   Order converters  — gobinance SDK types → exchange.OrderResult
//   Exit helpers      — slippage math for stop-limit exit orders

import (
	"errors"
	"strconv"
	"strings"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/common"
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

// ── Side mapping ──────────────────────────────────────────────────────────────

// binanceSide converts a gobinance SideType to an internal OrderSide string.
func binanceSide(s gobinance.SideType) exchange.OrderSide {
	if s == gobinance.SideTypeSell {
		return exchange.Sell
	}
	return exchange.Buy
}

// ── Order converters: Binance SDK → exchange.OrderResult ─────────────────────

// spotCreateToResult maps a spot CreateOrderResponse to OrderResult.
// Binance spot order IDs are encoded as "SYMBOL:numericID" to allow
// GetOrder / CancelOrder to recover both fields without extra state.
func spotCreateToResult(side exchange.OrderSide, r *gobinance.CreateOrderResponse) *exchange.OrderResult {
	filledQty := parseDecimal(r.ExecutedQuantity)
	quoteQty := parseDecimal(r.CummulativeQuoteQuantity)
	var filledAvg decimal.Decimal
	if filledQty.IsPositive() {
		filledAvg = quoteQty.Div(filledQty)
	}
	// Sum commission across all partial fills; take commissionAsset from the first fill.
	// All fills for a single order share the same commissionAsset.
	var totalCommission decimal.Decimal
	var commissionAsset string
	for _, f := range r.Fills {
		totalCommission = totalCommission.Add(parseDecimal(f.Commission))
		if commissionAsset == "" {
			commissionAsset = f.CommissionAsset
		}
	}
	return &exchange.OrderResult{
		ID:              r.Symbol + ":" + strconv.FormatInt(r.OrderID, 10),
		ClientOrderID:   r.ClientOrderID,
		Symbol:          r.Symbol,
		Side:            side,
		Status:          strings.ToLower(string(r.Status)),
		Qty:             parseDecimal(r.OrigQuantity),
		FilledQty:       filledQty,
		FilledAvg:       filledAvg,
		Commission:      totalCommission,
		CommissionAsset: commissionAsset,
	}
}

// spotGetToResult maps a spot GetOrderResponse to OrderResult.
func spotGetToResult(orderID string, r *gobinance.Order) *exchange.OrderResult {
	filledQty := parseDecimal(r.ExecutedQuantity)
	quoteQty := parseDecimal(r.CummulativeQuoteQuantity)
	var filledAvg decimal.Decimal
	if filledQty.IsPositive() {
		filledAvg = quoteQty.Div(filledQty)
	}
	return &exchange.OrderResult{
		ID:            orderID,
		ClientOrderID: r.ClientOrderID,
		Symbol:        r.Symbol,
		Side:          binanceSide(r.Side),
		Status:        strings.ToLower(string(r.Status)),
		Qty:           parseDecimal(r.OrigQuantity),
		FilledQty:     filledQty,
		FilledAvg:     filledAvg,
	}
}

// ── Exit order helpers ────────────────────────────────────────────────────────

// exitSlippageRate is the price buffer applied to stop-limit exit orders so they
// behave like near-market executions while avoiding a hard market order.
const exitSlippageRate = 0.005 // 0.5 %

// slippagePrice computes the stop-limit price for an exit order:
//   - Sell exit (closing long): stop is below trigger → subtract buffer.
//   - Buy  exit (closing short): stop is above trigger → add buffer.
func slippagePrice(stopTrigger decimal.Decimal, exitSide exchange.OrderSide) decimal.Decimal {
	one := decimal.NewFromInt(1)
	buf := decimal.NewFromFloat(exitSlippageRate)
	if exitSide == exchange.Sell {
		return stopTrigger.Mul(one.Sub(buf)).Round(2)
	}
	return stopTrigger.Mul(one.Add(buf)).Round(2)
}
