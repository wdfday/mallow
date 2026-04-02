package action

import (
	"context"
	"fmt"
)

// SpotBalance implements exchange.SpotTrader.
// Returns available balance for the given currency (e.g. "USDT").
func (c *Client) SpotBalance(ctx context.Context, asset string) (float64, error) {
	info, err := c.GetBalance(ctx)
	if err != nil {
		return 0, fmt.Errorf("okx spot balance %s: %w", asset, err)
	}
	for _, b := range info.Balances {
		if b.Currency == asset {
			return b.Available, nil
		}
	}
	return 0, nil // no balance = zero, not an error
}
