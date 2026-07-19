package act

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/adshao/go-binance/v2/futures"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// SyncAccount implements exchange.AccountSyncer for USDM futures.
func (c *Client) SyncAccount(ctx context.Context, creds exchange.Credentials, _ *time.Time) (*exchange.AccountSnapshot, error) {
	return c.syncFuturesUSDM(ctx, creds)
}

func (c *Client) syncFuturesUSDM(ctx context.Context, creds exchange.Credentials) (*exchange.AccountSnapshot, error) {
	acct, err := c.newFut(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("fbinance sync futures usdm: get account: %w", err)
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
		balances = append(balances, exchange.AssetBalance{
			Asset:         a.Asset,
			Free:          free,
			MarginBalance: parseDecimal(a.MarginBalance),
			UnrealizedPnL: parseDecimal(a.UnrealizedProfit),
		})
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
		markPx, priceErr := c.tickerPriceFutures(ctx, creds, p.Symbol)
		if priceErr != nil {
			slog.Warn("fbinance sync usdm: failed to get mark price", "symbol", p.Symbol, "err", priceErr)
			markPx = entryPx
		}
		qty := amt
		if qty.IsNegative() {
			qty = qty.Neg()
		}
		marginMode := "cross"
		if p.Isolated {
			marginMode = "isolated"
		}
		positions = append(positions, exchange.ExchangePosition{
			Symbol:           p.Symbol,
			Qty:              qty,
			AvgPrice:         entryPx,
			CurPrice:         markPx,
			Side:             fbinancePositionSide(p.PositionSide),
			UnrealizedPnL:    parseDecimal(p.UnrealizedProfit),
			Leverage:         parseDecimal(p.Leverage),
			LiquidationPrice: decimal.Zero, // not returned by /fapi/v2/account; only /fapi/v2/positionRisk has it
			MarginMode:       marginMode,
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
		// AccountEquity is Binance's own margin-adjusted total (wallet balance +
		// unrealized PnL across the account) — already returned by this same
		// endpoint, just unused until now.
		AccountEquity: parseDecimal(acct.TotalMarginBalance),
	}, nil
}

// fbinancePositionSide maps go-binance's uppercase futures.PositionSideType
// ("LONG"/"SHORT"/"BOTH") to exchange.PositionSide's lowercase convention.
func fbinancePositionSide(s futures.PositionSideType) exchange.PositionSide {
	switch s {
	case futures.PositionSideTypeLong:
		return exchange.PositionLong
	case futures.PositionSideTypeShort:
		return exchange.PositionShort
	default:
		return exchange.PositionNet
	}
}

// tickerPriceFutures fetches the latest price for a symbol from the USDM futures endpoint.
func (c *Client) tickerPriceFutures(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	prices, err := c.newFut(creds).NewListPricesService().Symbol(symbol).Do(ctx)
	if err != nil {
		return decimal.Zero, fmt.Errorf("fbinance futures list prices %s: %w", symbol, err)
	}
	for _, p := range prices {
		if strings.EqualFold(p.Symbol, symbol) {
			return parseDecimal(p.Price), nil
		}
	}
	return decimal.Zero, fmt.Errorf("fbinance: symbol %s not found in futures ticker response", symbol)
}

// GetCurrentPrice implements exchange.PriceFetcher via futures ticker price.
func (c *Client) GetCurrentPrice(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	price, err := c.tickerPriceFutures(ctx, creds, symbol)
	if err != nil {
		return decimal.Zero, fmt.Errorf("fbinance get price %s: %w", symbol, err)
	}
	return price, nil
}
