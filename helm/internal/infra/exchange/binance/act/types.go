package act

// types.go — Binance native types and converters to internal exchange types.
//
// Layout:
//   Parse helpers     — string → decimal / float64
//   Side mapping      — gobinance.SideType ↔ exchange.OrderSide
//   Order converters  — gobinance / futures SDK types → exchange.OrderResult
//   Exit helpers      — slippage math for stop-limit exit orders

import (
	"strconv"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

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
	return &exchange.OrderResult{
		ID:        r.Symbol + ":" + strconv.FormatInt(r.OrderID, 10),
		Symbol:    r.Symbol,
		Side:      side,
		Status:    string(r.Status),
		Qty:       parseDecimal(r.OrigQuantity),
		FilledQty: filledQty,
		FilledAvg: filledAvg,
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
		ID:        orderID,
		Symbol:    r.Symbol,
		Side:      binanceSide(r.Side),
		Status:    string(r.Status),
		Qty:       parseDecimal(r.OrigQuantity),
		FilledQty: filledQty,
		FilledAvg: filledAvg,
	}
}

// futuresCreateToResult maps a futures CreateOrderResponse to OrderResult.
// Futures order IDs are plain numeric strings (no symbol prefix needed —
// futures GetOrder accepts symbol + orderID separately).
func futuresCreateToResult(side exchange.OrderSide, r *futures.CreateOrderResponse) *exchange.OrderResult {
	return &exchange.OrderResult{
		ID:        strconv.FormatInt(r.OrderID, 10),
		Symbol:    r.Symbol,
		Side:      side,
		Status:    string(r.Status),
		Qty:       parseDecimal(r.OrigQuantity),
		FilledQty: parseDecimal(r.ExecutedQuantity),
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
		return stopTrigger.Mul(one.Sub(buf))
	}
	return stopTrigger.Mul(one.Add(buf))
}
