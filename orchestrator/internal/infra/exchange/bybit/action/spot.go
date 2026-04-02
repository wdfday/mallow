package action

import (
	"context"
	"fmt"
)

// SpotBalance implements exchange.SpotTrader.
// Returns available balance for the given coin from the SPOT wallet.
func (c *Client) SpotBalance(ctx context.Context, asset string) (float64, error) {
	info, err := c.GetWalletBalance(ctx, "SPOT")
	if err != nil {
		return 0, fmt.Errorf("bybit spot balance %s: %w", asset, err)
	}
	for _, coin := range info.Coins {
		if coin.Coin == asset {
			return coin.Free, nil
		}
	}
	return 0, nil // no balance = zero
}
