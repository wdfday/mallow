package action

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

// GetCurrentPrice implements exchange.PriceFetcher via Binance spot REST ticker.
func (c *Client) GetCurrentPrice(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	price, err := c.tickerPrice(ctx, creds, symbol)
	if err != nil {
		return decimal.Zero, fmt.Errorf("binance get price %s: %w", symbol, err)
	}
	return price, nil
}
