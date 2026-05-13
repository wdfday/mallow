package act

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// SpotBalance returns available balance for the given currency (e.g. "USDT").
func (c *Client) SpotBalance(ctx context.Context, creds exchange.Credentials, asset string) (decimal.Decimal, error) {
	info, err := c.GetBalance(ctx, creds)
	if err != nil {
		return decimal.Zero, fmt.Errorf("okx spot balance %s: %w", asset, err)
	}
	for _, b := range info.Balances {
		if b.Currency == asset {
			return decimal.NewFromFloat(b.Available), nil
		}
	}
	return decimal.Zero, nil
}
