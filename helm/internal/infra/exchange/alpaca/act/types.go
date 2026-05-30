package act

// types.go — Alpaca SDK native types and converters to internal exchange types.
//
// Layout:
//   Order converter  — alpacasdk.Order → exchange.OrderResult

import (
	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"

	"mallow/helm/internal/infra/exchange"
)

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
