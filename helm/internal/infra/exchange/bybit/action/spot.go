package action

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// SpotBalance implements exchange.SpotTrader.
// Returns available balance for the given coin from the SPOT wallet.
func (c *Client) SpotBalance(ctx context.Context, creds exchange.Credentials, asset string) (decimal.Decimal, error) {
	info, err := c.GetWalletBalance(ctx, creds, "SPOT")
	if err != nil {
		return decimal.Zero, fmt.Errorf("bybit spot balance %s: %w", asset, err)
	}
	for _, coin := range info.Coins {
		if coin.Coin == asset {
			return decimal.NewFromFloat(coin.Free), nil
		}
	}
	return decimal.Zero, nil // no balance = zero
}
