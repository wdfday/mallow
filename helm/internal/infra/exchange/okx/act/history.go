package act

import (
	"context"
	"fmt"
	"time"

	"mallow/helm/internal/infra/exchange"
)

// FilledOrders implements exchange.HistoryFetcher.
// OKX supports cross-symbol queries; symbols is ignored.
// For unified accounts, queries both SPOT and SWAP inst types.
func (c *Client) FilledOrders(ctx context.Context, creds exchange.Credentials, _ []string, from, to time.Time) ([]exchange.AccountTransaction, error) {
	instTypes := []string{"SPOT"}
	if creds.AccountType == exchange.AccountFuturesUSDM || creds.AccountType == exchange.AccountUnified {
		instTypes = append(instTypes, "SWAP")
	}

	var all []exchange.AccountTransaction
	for _, instType := range instTypes {
		path := fmt.Sprintf("/api/v5/trade/fills-history?instType=%s&begin=%d&end=%d&limit=100",
			instType, from.UnixMilli(), to.UnixMilli())
		var resp fillsHistoryResp
		if err := c.doRequest(ctx, creds, "GET", path, nil, &resp); err != nil {
			return nil, fmt.Errorf("okx fills-history %s: %w", instType, err)
		}
		if resp.Code != "0" {
			return nil, fmt.Errorf("okx fills-history %s: code=%s msg=%s", instType, resp.Code, resp.Msg)
		}
		for _, f := range resp.Data {
			tsMs := parseTimestampMs(f.TS)
			fee := parseDecimal(f.Fee)
			if fee.IsNegative() {
				fee = fee.Neg()
			}
			all = append(all, exchange.AccountTransaction{
				TradeID:       f.TradeID,
				OrderID:       f.OrdID,
				ClientOrderID: f.ClOrdID,
				Symbol:        f.InstID,
				Side:          f.Side,
				Qty:           parseDecimal(f.FillSz),
				AvgPrice:      parseDecimal(f.FillPx),
				Fee:           fee,
				FeeAsset:      f.FeeCcy,
				FilledAt:      time.UnixMilli(tsMs).UTC(),
			})
		}
	}
	return all, nil
}
