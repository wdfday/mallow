package act

import (
	"context"
	"fmt"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"

	"mallow/helm/internal/infra/exchange"
)

// ListOpenOrders returns all open (pending) orders.
// When symbol is non-empty, only orders for that symbol are returned.
func (c *Client) ListOpenOrders(_ context.Context, creds exchange.Credentials, symbol string) ([]exchange.OrderResult, error) {
	req := alpacasdk.GetOrdersRequest{Status: "open", Limit: 500}
	if symbol != "" {
		req.Symbols = []string{symbol}
	}
	orders, err := c.newSDK(creds).GetOrders(req)
	if err != nil {
		return nil, fmt.Errorf("alpaca list open orders: %w", err)
	}
	results := make([]exchange.OrderResult, len(orders))
	for i := range orders {
		results[i] = *mapOrder(&orders[i])
	}
	return results, nil
}

// ListPositions returns all currently held positions.
func (c *Client) ListPositions(_ context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	positions, err := c.newSDK(creds).GetPositions()
	if err != nil {
		return nil, fmt.Errorf("alpaca list positions: %w", err)
	}
	results := make([]exchange.PositionResult, 0, len(positions))
	for _, p := range positions {
		side := exchange.Buy
		if p.Side == "short" {
			side = exchange.Sell
		}
		qty := p.Qty
		if qty.IsNegative() {
			qty = qty.Neg()
		}
		r := exchange.PositionResult{
			Symbol:   p.Symbol,
			Side:     side,
			Qty:      qty,
			AvgPrice: p.AvgEntryPrice,
		}
		if p.UnrealizedPL != nil {
			r.UnrealPnL = *p.UnrealizedPL
		}
		results = append(results, r)
	}
	return results, nil
}
