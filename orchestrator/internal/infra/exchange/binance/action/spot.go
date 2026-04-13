package action

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

// SpotBalance returns the free balance of the given asset from the spot account.
func (c *Client) SpotBalance(ctx context.Context, creds exchange.Credentials, asset string) (decimal.Decimal, error) {
	info, err := c.GetBalance(ctx, creds, asset)
	if err != nil {
		return decimal.Zero, fmt.Errorf("binance spot balance %s: %w", asset, err)
	}
	return decimal.NewFromFloat(info.Free), nil
}
