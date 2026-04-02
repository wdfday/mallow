package action

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"orchestrator/internal/infra/exchange"
)

// SyncAccount implements exchange.AccountSyncer for Binance spot.
// Cash = sum of stablecoin free balances (USDT/BUSD/USDC).
// Positions = non-stablecoin assets priced via ticker.
func (c *Client) SyncAccount(ctx context.Context, _ *time.Time) (*exchange.AccountSnapshot, error) {
	acct, err := c.spot.NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance sync: get account: %w", err)
	}

	cash := 0.0
	type assetBalance struct {
		asset string
		free  float64
	}
	var nonCash []assetBalance

	for _, b := range acct.Balances {
		free := parseFloat(b.Free)
		if free <= 0 {
			continue
		}
		if b.Asset == "USDT" || b.Asset == "BUSD" || b.Asset == "USDC" {
			cash += free
		} else {
			nonCash = append(nonCash, assetBalance{asset: b.Asset, free: free})
		}
	}

	positions := make([]exchange.ExchangePosition, 0, len(nonCash))
	for _, ab := range nonCash {
		symbol := ab.asset + "USDT"
		price, err := c.tickerPrice(ctx, symbol)
		if err != nil {
			slog.Warn("binance sync: failed to get ticker price", "symbol", symbol, "err", err)
		}
		positions = append(positions, exchange.ExchangePosition{
			Symbol:   symbol,
			Qty:      ab.free,
			AvgPrice: 0, // spot REST does not expose avg entry price
			CurPrice: price,
		})
	}

	mv := 0.0
	for _, p := range positions {
		mv += p.Qty * p.CurPrice
	}

	return &exchange.AccountSnapshot{
		Cash:      cash,
		Equity:    cash + mv,
		Positions: positions,
	}, nil
}

// tickerPrice fetches the latest spot price for a symbol.
func (c *Client) tickerPrice(ctx context.Context, symbol string) (float64, error) {
	prices, err := c.spot.NewListPricesService().Symbol(symbol).Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("list prices %s: %w", symbol, err)
	}
	for _, p := range prices {
		if strings.EqualFold(p.Symbol, symbol) {
			return parseFloat(p.Price), nil
		}
	}
	return 0, fmt.Errorf("symbol %s not found in ticker response", symbol)
}
