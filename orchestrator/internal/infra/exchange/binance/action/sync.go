package action

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

// SyncAccount implements exchange.AccountSyncer for Binance spot.
func (c *Client) SyncAccount(ctx context.Context, creds exchange.Credentials, _ *time.Time) (*exchange.AccountSnapshot, error) {
	spot := c.newSpot(creds)
	acct, err := spot.NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance sync: get account: %w", err)
	}

	cash := decimal.Zero
	type assetBalance struct {
		asset string
		free  decimal.Decimal
	}
	var nonCash []assetBalance

	for _, b := range acct.Balances {
		free := parseDecimal(b.Free)
		if !free.IsPositive() {
			continue
		}
		if b.Asset == "USDT" || b.Asset == "BUSD" || b.Asset == "USDC" {
			cash = cash.Add(free)
		} else {
			nonCash = append(nonCash, assetBalance{asset: b.Asset, free: free})
		}
	}

	positions := make([]exchange.ExchangePosition, 0, len(nonCash))
	for _, ab := range nonCash {
		symbol := ab.asset + "USDT"
		price, err := c.tickerPrice(ctx, creds, symbol)
		if err != nil {
			slog.Warn("binance sync: failed to get ticker price", "symbol", symbol, "err", err)
		}
		positions = append(positions, exchange.ExchangePosition{
			Symbol:   symbol,
			Qty:      ab.free,
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
	}, nil
}

// tickerPrice fetches the latest spot price for a symbol.
func (c *Client) tickerPrice(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	prices, err := c.newSpot(creds).NewListPricesService().Symbol(symbol).Do(ctx)
	if err != nil {
		return decimal.Zero, fmt.Errorf("list prices %s: %w", symbol, err)
	}
	for _, p := range prices {
		if strings.EqualFold(p.Symbol, symbol) {
			return parseDecimal(p.Price), nil
		}
	}
	return decimal.Zero, fmt.Errorf("symbol %s not found in ticker response", symbol)
}
