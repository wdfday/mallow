package act

import (
	"context"
	"fmt"
	"time"

	"mallow/helm/internal/infra/exchange"
)

// FilledOrders implements exchange.HistoryFetcher.
// Bybit supports cross-symbol queries; symbols is ignored.
func (c *Client) FilledOrders(ctx context.Context, creds exchange.Credentials, _ []string, from, to time.Time) ([]exchange.AccountTransaction, error) {
	path := fmt.Sprintf("/v5/execution/list?category=spot&startTime=%d&endTime=%d&limit=200",
		from.UnixMilli(), to.UnixMilli())

	var resp apiResponse[execListResult]
	if err := c.doSigned(ctx, creds, "GET", path, nil, &resp); err != nil {
		return nil, fmt.Errorf("bybit execution/list: %w", err)
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit execution/list: retCode=%d msg=%s", resp.RetCode, resp.RetMsg)
	}

	all := make([]exchange.AccountTransaction, 0, len(resp.Result.List))
	for _, e := range resp.Result.List {
		tsMs := parseTimestampMs(e.ExecTime)
		side := "buy"
		if e.Side == "Sell" {
			side = "sell"
		}
		all = append(all, exchange.AccountTransaction{
			TradeID:  e.ExecID,
			OrderID:  e.OrderID,
			Symbol:   e.Symbol,
			Side:     side,
			Qty:      parseDecimal(e.ExecQty),
			AvgPrice: parseDecimal(e.ExecPrice),
			Fee:      parseDecimal(e.ExecFee),
			FilledAt: time.UnixMilli(tsMs).UTC(),
		})
	}
	return all, nil
}
