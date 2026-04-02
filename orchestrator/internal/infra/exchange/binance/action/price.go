package action

import (
	"context"
	"fmt"
)

// GetCurrentPrice implements exchange.PriceFetcher via Binance spot REST ticker.
func (c *Client) GetCurrentPrice(ctx context.Context, symbol string) (float64, error) {
	price, err := c.tickerPrice(ctx, symbol)
	if err != nil {
		return 0, fmt.Errorf("binance get price %s: %w", symbol, err)
	}
	return price, nil
}
