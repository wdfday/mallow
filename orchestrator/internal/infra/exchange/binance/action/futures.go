package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/adshao/go-binance/v2/futures"
)

// SupportsFutures implements exchange.FuturesTrader — Binance supports futures.
func (c *Client) SupportsFutures() bool { return true }

// SetLeverage sets the leverage for a futures symbol.
// marginType: "ISOLATED" | "CROSSED"
func (c *Client) SetLeverage(ctx context.Context, symbol string, leverage int, marginType string) error {
	_, err := c.fut.NewChangeLeverageService().
		Symbol(symbol).
		Leverage(leverage).
		Do(ctx)
	if err != nil {
		return fmt.Errorf("binance set leverage %s x%d: %w", symbol, leverage, err)
	}

	mt := futures.MarginTypeIsolated
	if strings.ToUpper(marginType) == "CROSSED" {
		mt = futures.MarginTypeCrossed
	}
	err = c.fut.NewChangeMarginTypeService().
		Symbol(symbol).
		MarginType(mt).
		Do(ctx)
	if err != nil {
		// Binance returns -4046 when margin type is already set — treat as non-fatal.
		if strings.Contains(err.Error(), "-4046") {
			return nil
		}
		return fmt.Errorf("binance set margin type %s %s: %w", symbol, marginType, err)
	}
	return nil
}

// FundingRate returns the latest funding rate for a futures symbol.
func (c *Client) FundingRate(ctx context.Context, symbol string) (float64, error) {
	rates, err := c.fut.NewFundingRateService().Symbol(symbol).Limit(1).Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("binance funding rate %s: %w", symbol, err)
	}
	if len(rates) == 0 {
		return 0, fmt.Errorf("binance: no funding rate data for %s", symbol)
	}
	return parseFloat(rates[0].FundingRate), nil
}

// MarkPrice returns the current mark price for a futures symbol.
func (c *Client) MarkPrice(ctx context.Context, symbol string) (float64, error) {
	prices, err := c.fut.NewPremiumIndexService().Symbol(symbol).Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("binance mark price %s: %w", symbol, err)
	}
	for _, p := range prices {
		if p.Symbol == symbol {
			return parseFloat(p.MarkPrice), nil
		}
	}
	return 0, fmt.Errorf("binance: mark price not found for %s", symbol)
}
