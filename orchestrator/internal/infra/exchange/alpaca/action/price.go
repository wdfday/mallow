package action

import (
	"context"
	"fmt"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
)

// GetCurrentPrice implements exchange.PriceFetcher.
// Uses the Alpaca marketdata REST API to fetch the latest trade price.
// Works for both paper and live accounts (marketdata endpoint is shared).
func (c *Client) GetCurrentPrice(_ context.Context, symbol string) (float64, error) {
	var feed string
	if isCrypto(symbol) {
		feed = "us"
	}
	trade, err := c.md.GetLatestTrade(symbol, marketdata.GetLatestTradeRequest{Feed: feed})
	if err != nil {
		return 0, fmt.Errorf("alpaca get price %s: %w", symbol, err)
	}
	return trade.Price, nil
}
