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

// SyncAccount implements exchange.AccountSyncer.
// Dispatches to spot or futures path based on creds.AccountType.
func (c *Client) SyncAccount(ctx context.Context, creds exchange.Credentials, _ *time.Time) (*exchange.AccountSnapshot, error) {
	switch creds.AccountType {
	case exchange.AccountFuturesUSDM:
		return c.syncFuturesUSDM(ctx, creds)
	default:
		return c.syncSpot(ctx, creds)
	}
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
		free  decimal.Decimal
	}
	var nonCash []assetBalance
	var balances []exchange.AssetBalance

	for _, b := range acct.Balances {
		free := parseDecimal(b.Free)
		if !free.IsPositive() {
			continue
		}
		balances = append(balances, exchange.AssetBalance{Asset: b.Asset, Free: free})
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
		Balances:  balances,
	}, nil
}

// syncFuturesUSDM handles USDM-margined futures account sync.
func (c *Client) syncFuturesUSDM(ctx context.Context, creds exchange.Credentials) (*exchange.AccountSnapshot, error) {
	acct, err := c.newFut(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance sync futures: get account: %w", err)
	}

	cash := decimal.Zero
	var balances []exchange.AssetBalance
	for _, a := range acct.Assets {
		avail := parseDecimal(a.AvailableBalance)
		wallet := parseDecimal(a.WalletBalance)
		if !avail.IsPositive() && !wallet.IsPositive() {
			continue
		}
		free := avail
		if wallet.GreaterThan(free) {
			free = wallet
		}
		balances = append(balances, exchange.AssetBalance{Asset: a.Asset, Free: free})
		if a.Asset == "USDT" || a.Asset == "BUSD" || a.Asset == "USDC" {
			cash = cash.Add(avail)
		}
	}

	positions := make([]exchange.ExchangePosition, 0, len(acct.Positions))
	for _, p := range acct.Positions {
		amt := parseDecimal(p.PositionAmt)
		if amt.IsZero() {
			continue
		}
		entryPx := parseDecimal(p.EntryPrice)
		// Mark price is not in AccountPosition; use a ticker call.
		markPx, priceErr := c.tickerPrice(ctx, creds, p.Symbol)
		if priceErr != nil {
			slog.Warn("binance sync futures: failed to get mark price", "symbol", p.Symbol, "err", priceErr)
			markPx = entryPx // fallback to entry price
		}
		qty := amt
		if qty.IsNegative() {
			qty = qty.Neg()
		}
		positions = append(positions, exchange.ExchangePosition{
			Symbol:   p.Symbol,
			Qty:      qty,
			AvgPrice: entryPx,
			CurPrice: markPx,
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
