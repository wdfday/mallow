package act

import (
	"context"
	"fmt"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"

	"mallow/helm/internal/infra/exchange"
)

// FilledOrders implements exchange.HistoryFetcher.
// Queries closed orders in [from, to) and returns those with filled status.
func (c *Client) FilledOrders(ctx context.Context, creds exchange.Credentials, _ []string, from, to time.Time) ([]exchange.AccountTransaction, error) {
	sdk := c.newSDK(creds)
	orders, err := sdk.GetOrders(alpacasdk.GetOrdersRequest{
		Status:    "closed",
		After:     from,
		Until:     to,
		Direction: "asc",
		Limit:     500,
	})
	if err != nil {
		return nil, fmt.Errorf("alpaca history orders: %w", err)
	}

	all := make([]exchange.AccountTransaction, 0, len(orders))
	for _, o := range orders {
		if o.Status != "filled" && o.Status != "partially_filled" {
			continue
		}
		if !o.FilledQty.IsPositive() || o.FilledAvgPrice == nil {
			continue
		}
		side := "buy"
		if o.Side == alpacasdk.Sell {
			side = "sell"
		}
		filledAt := time.Now().UTC()
		if o.FilledAt != nil {
			filledAt = o.FilledAt.UTC()
		}
		all = append(all, exchange.AccountTransaction{
			TradeID:  o.ID,
			OrderID:  o.ID,
			Symbol:   o.Symbol,
			Side:     side,
			Qty:      o.FilledQty,
			AvgPrice: *o.FilledAvgPrice,
			FilledAt: filledAt,
		})
	}
	return all, nil
}
