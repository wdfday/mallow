package action

import (
	"context"
	"fmt"
)

// SupportsFutures implements exchange.FuturesTrader — Alpaca is spot-only.
func (c *Client) SupportsFutures() bool { return false }

func (c *Client) SetLeverage(_ context.Context, symbol string, _ int, _ string) error {
	return fmt.Errorf("alpaca: futures not supported (symbol: %s)", symbol)
}

func (c *Client) FundingRate(_ context.Context, symbol string) (float64, error) {
	return 0, fmt.Errorf("alpaca: futures not supported (symbol: %s)", symbol)
}

func (c *Client) MarkPrice(_ context.Context, symbol string) (float64, error) {
	return 0, fmt.Errorf("alpaca: futures not supported (symbol: %s)", symbol)
}
