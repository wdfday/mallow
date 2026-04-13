package action

import (
	"context"
	"fmt"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

// GetCurrentPrice implements exchange.PriceFetcher via Alpaca market data REST (public endpoint).
func (c *Client) GetCurrentPrice(_ context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	var feed string
	if isCrypto(symbol) {
		feed = "us"
	}
	trade, err := c.newMD(creds).GetLatestTrade(symbol, marketdata.GetLatestTradeRequest{Feed: feed})
	if err != nil {
		return decimal.Zero, fmt.Errorf("alpaca get price %s: %w", symbol, err)
	}
	return decimal.NewFromFloat(trade.Price), nil
}
