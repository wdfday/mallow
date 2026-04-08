package action

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
)

// GetCurrentPrice implements exchange.PriceFetcher via Binance spot REST ticker.
func (c *Client) GetCurrentPrice(ctx context.Context, symbol string) (decimal.Decimal, error) {
	price, err := c.tickerPrice(ctx, symbol)
	if err != nil {
		return decimal.Zero, fmt.Errorf("binance get price %s: %w", symbol, err)
	}
	return price, nil
}
