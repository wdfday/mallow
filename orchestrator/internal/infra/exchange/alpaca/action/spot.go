package action

import (
	"context"
	"fmt"

	"orchestrator/internal/infra/exchange"
)

// SpotBalance implements exchange.SpotTrader.
// Returns the available cash balance for the given asset.
// Alpaca accounts are USD-denominated; other assets return their position market value.
func (c *Client) SpotBalance(ctx context.Context, creds exchange.Credentials, asset string) (float64, error) {
	sdk := c.newSDK(creds)
	acct, err := sdk.GetAccount()
	if err != nil {
		return 0, fmt.Errorf("alpaca spot balance: %w", err)
	}
	switch asset {
	case "USD", "":
		return acct.Cash.InexactFloat64(), nil
	default:
		// For non-USD assets, return the market value of the position.
		pos, err := sdk.GetPosition(asset)
		if err != nil {
			return 0, nil // no position = zero balance
		}
		if pos.MarketValue != nil {
			return pos.MarketValue.InexactFloat64(), nil
		}
		return 0, nil
	}
}
