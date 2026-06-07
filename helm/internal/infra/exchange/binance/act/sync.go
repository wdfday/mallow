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
// Dispatches to spot, USDM futures, or COINM futures based on creds.AccountType.
func (c *Client) SyncAccount(ctx context.Context, creds exchange.Credentials, _ *time.Time) (*exchange.AccountSnapshot, error) {
	switch creds.AccountType {
	case exchange.AccountFuturesUSDM:
		return c.syncFuturesUSDM(ctx, creds)
	case exchange.AccountFuturesCOINM:
		return c.syncFuturesCOINM(ctx, creds)
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

// syncFuturesUSDM handles USDM-margined futures account sync (FAPI — settled in USDT/USDC).
func (c *Client) syncFuturesUSDM(ctx context.Context, creds exchange.Credentials) (*exchange.AccountSnapshot, error) {
	acct, err := c.newFut(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance sync futures usdm: get account: %w", err)
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
		if a.Asset == "USDT" {
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
		// Use futures mark price, not spot ticker.
		markPx, priceErr := c.tickerPriceFutures(ctx, creds, p.Symbol)
		if priceErr != nil {
			slog.Warn("binance sync usdm: failed to get mark price", "symbol", p.Symbol, "err", priceErr)
			markPx = entryPx
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

// syncFuturesCOINM handles COIN-M-margined futures account sync (DAPI — settled in base coin).
func (c *Client) syncFuturesCOINM(ctx context.Context, creds exchange.Credentials) (*exchange.AccountSnapshot, error) {
	acct, err := c.newDelivery(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance sync futures coinm: get account: %w", err)
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
		// COIN-M margin is in BTC/ETH/etc — treat USDT equivalent as 0 for cash
		// (wallet is in coin, equity is computed from positions mark price).
	}

	positions := make([]exchange.ExchangePosition, 0, len(acct.Positions))
	for _, p := range acct.Positions {
		amt := parseDecimal(p.PositionAmt)
		if amt.IsZero() {
			continue
		}
		entryPx := parseDecimal(p.EntryPrice)
		markPx, priceErr := c.tickerPriceDelivery(ctx, creds, p.Symbol)
		if priceErr != nil {
			slog.Warn("binance sync coinm: failed to get mark price", "symbol", p.Symbol, "err", priceErr)
			markPx = entryPx
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

	// For COIN-M, cash is wallet balance in USDT terms (approximated via mark price if any).
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

// tickerPriceFutures fetches the latest price for a symbol from the USDM futures endpoint.
func (c *Client) tickerPriceFutures(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	prices, err := c.newFut(creds).NewListPricesService().Symbol(symbol).Do(ctx)
	if err != nil {
		return decimal.Zero, fmt.Errorf("futures list prices %s: %w", symbol, err)
	}
	for _, p := range prices {
		if strings.EqualFold(p.Symbol, symbol) {
			return parseDecimal(p.Price), nil
		}
	}
	return decimal.Zero, fmt.Errorf("symbol %s not found in futures ticker response", symbol)
}

// tickerPriceDelivery fetches the latest price for a symbol from the COINM delivery endpoint.
func (c *Client) tickerPriceDelivery(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	prices, err := c.newDelivery(creds).NewListPricesService().Symbol(symbol).Do(ctx)
	if err != nil {
		return decimal.Zero, fmt.Errorf("delivery list prices %s: %w", symbol, err)
	}
	for _, p := range prices {
		if strings.EqualFold(p.Symbol, symbol) {
			return parseDecimal(p.Price), nil
		}
	}
	return decimal.Zero, fmt.Errorf("symbol %s not found in delivery ticker response", symbol)
}

// tickerPrice kept for backward compat — routes to spot (used by reconcile/history).
func (c *Client) tickerPrice(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	return c.tickerPriceSpot(ctx, creds, symbol)
}
