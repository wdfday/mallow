package act

import (
	"context"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

var _ exchange.SpotBalanceFetcher = (*Client)(nil)

// GetFreeBalance implements exchange.SpotBalanceFetcher.
// Returns the available (unfrozen) balance for the given currency (e.g. "USDT", "BTC").
// Used as a fallback in the insufficient-balance exit retry path.
func (c *Client) GetFreeBalance(ctx context.Context, creds exchange.Credentials, asset string) (decimal.Decimal, error) {
	info, err := c.GetBalance(ctx, creds)
	if err != nil {
		return decimal.Zero, err
	}
	for _, b := range info.Balances {
		if b.Currency == asset {
			return decimal.NewFromFloat(b.Available), nil
		}
	}
	return decimal.Zero, nil
}
