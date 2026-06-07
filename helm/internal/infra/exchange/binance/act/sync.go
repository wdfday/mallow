package act

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// SyncAccount implements exchange.AccountSyncer — spot only.
func (c *Client) SyncAccount(ctx context.Context, creds exchange.Credentials, _ *time.Time) (*exchange.AccountSnapshot, error) {
	return c.syncSpot(ctx, creds)
}

// syncSpot handles spot account sync: balances → synthetic positions.
func (c *Client) syncSpot(ctx context.Context, creds exchange.Credentials) (*exchange.AccountSnapshot, error) {
	acct, err := c.newSpot(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance sync spot: get account: %w", err)
	}

	cash := decimal.Zero
	type assetBalance struct {
		asset string
		total decimal.Decimal
	}
	var nonCash []assetBalance
	var balances []exchange.AssetBalance

	for _, b := range acct.Balances {
		free := parseDecimal(b.Free)
		locked := parseDecimal(b.Locked)
		total := free.Add(locked)
		if !total.IsPositive() {
			continue
		}
		if free.IsPositive() {
			balances = append(balances, exchange.AssetBalance{Asset: b.Asset, Free: free})
		}
		if b.Asset == "USDT" {
			cash = cash.Add(free)
		} else if b.Asset != "BUSD" && b.Asset != "USDC" {
			nonCash = append(nonCash, assetBalance{asset: b.Asset, total: total})
		}
	}

	positions := make([]exchange.ExchangePosition, 0, len(nonCash))
	for _, ab := range nonCash {
		symbol := ab.asset + "USDT"
		price, err := c.tickerPriceSpot(ctx, creds, symbol)
		if err != nil {
			slog.Warn("binance sync: failed to get ticker price", "symbol", symbol, "err", err)
		}
		positions = append(positions, exchange.ExchangePosition{
			Symbol:   symbol,
			Qty:      ab.total,
			AvgPrice: decimal.Zero,
			CurPrice: price,
		})
	}

	mv := decimal.Zero
	for _, p := range positions {
		mv = mv.Add(p.Qty.Mul(p.CurPrice))
	}

	return &exchange.AccountSnapshot{
		Cash:      cash,
		Equity:    cash.Add(mv),
		Positions: positions,
		Balances:  balances,
	}, nil
}

// tickerPriceSpot fetches the latest price for a symbol from the spot endpoint.
func (c *Client) tickerPriceSpot(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	prices, err := c.newSpot(creds).NewListPricesService().Symbol(symbol).Do(ctx)
	if err != nil {
		return decimal.Zero, fmt.Errorf("spot list prices %s: %w", symbol, err)
	}
	for _, p := range prices {
		if strings.EqualFold(p.Symbol, symbol) {
			return parseDecimal(p.Price), nil
		}
	}
	return decimal.Zero, fmt.Errorf("symbol %s not found in spot ticker response", symbol)
}

// tickerPrice is kept for backward compat — routes to spot.
func (c *Client) tickerPrice(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	return c.tickerPriceSpot(ctx, creds, symbol)
}
