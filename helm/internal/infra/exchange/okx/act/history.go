package act

import (
	"context"
	"fmt"
	"time"

	"mallow/helm/internal/infra/exchange"
)

const fillsPageSize = 100

// FilledOrders implements exchange.HistoryFetcher.
// OKX supports cross-symbol queries; the symbols parameter is ignored.
// For unified accounts, queries both SPOT and SWAP inst types.
// Paginates using OKX's cursor-based after= parameter until all fills are fetched.
func (c *Client) FilledOrders(ctx context.Context, creds exchange.Credentials, _ []string, from, to time.Time) ([]exchange.AccountTransaction, error) {
	instTypes := []string{"SPOT"}
	if creds.AccountType == exchange.AccountFuturesUSDM || creds.AccountType == exchange.AccountUnified {
		instTypes = append(instTypes, "SWAP")
	}

	var all []exchange.AccountTransaction
	for _, instType := range instTypes {
		txns, err := c.fetchAllFills(ctx, creds, instType, from, to)
		if err != nil {
			return nil, err
		}
		all = append(all, txns...)
	}
	return all, nil
}

// fetchAllFills fetches all fills for one instType, paginating via OKX cursor (after=billId).
func (c *Client) fetchAllFills(ctx context.Context, creds exchange.Credentials, instType string, from, to time.Time) ([]exchange.AccountTransaction, error) {
	var all []exchange.AccountTransaction
	after := "" // empty = start from most recent

	for {
		path := fmt.Sprintf("/api/v5/trade/fills-history?instType=%s&begin=%d&end=%d&limit=%d",
			instType, from.UnixMilli(), to.UnixMilli(), fillsPageSize)
		if after != "" {
			path += "&after=" + after
		}

		var resp fillsHistoryResp
		if err := c.doRequest(ctx, creds, "GET", path, nil, &resp); err != nil {
			return nil, fmt.Errorf("okx fills-history %s: %w", instType, err)
		}
		if resp.Code != "0" {
			return nil, fmt.Errorf("okx fills-history %s: code=%s msg=%s", instType, resp.Code, resp.Msg)
		}
		if len(resp.Data) == 0 {
			break
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

		// OKX returns results newest-first. Paginate if we got a full page.
		// Use the billId of the last (oldest) record as the cursor for the next page.
		if len(resp.Data) < fillsPageSize {
			break
		}
		after = resp.Data[len(resp.Data)-1].BillID
		if after == "" {
			break // no cursor available — stop to avoid infinite loop
		}
	}
	return all, nil
}
