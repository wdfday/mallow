package action

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
)

// SpotBalance implements exchange.SpotTrader.
// Returns the free balance of the given asset from the spot account.
// For stablecoins (USDT/USDC/BUSD) this is the cash balance.
func (c *Client) SpotBalance(ctx context.Context, asset string) (decimal.Decimal, error) {
	info, err := c.GetBalance(ctx, asset)
	if err != nil {
		return decimal.Zero, fmt.Errorf("binance spot balance %s: %w", asset, err)
	}
	return decimal.NewFromFloat(info.Free), nil
}
