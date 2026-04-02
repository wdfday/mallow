package action

import (
	"context"
	"fmt"
)

// SpotBalance implements exchange.SpotTrader.
// Returns the free balance of the given asset from the spot account.
// For stablecoins (USDT/USDC/BUSD) this is the cash balance.
func (c *Client) SpotBalance(ctx context.Context, asset string) (float64, error) {
	info, err := c.GetBalance(ctx, asset)
	if err != nil {
		return 0, fmt.Errorf("binance spot balance %s: %w", asset, err)
	}
	return info.Free, nil
}
