package act

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// BalanceInfo represents a single asset balance.
type BalanceInfo struct {
	Asset  string
	Free   float64
	Locked float64
}

// AccountInfo is a simplified view of the Binance account.
type AccountInfo struct {
	CanTrade    bool
	CanWithdraw bool
	CanDeposit  bool
	AccountType string
	Balances    []BalanceInfo
}

// ExchangeAsset contains info about a single trading pair.
type ExchangeAsset struct {
	Symbol     string
	BaseAsset  string
	QuoteAsset string
	Status     string
	Tradable   bool
}

// GetAccount returns account info including all non-zero balances.
func (c *Client) GetAccount(ctx context.Context, creds exchange.Credentials) (*AccountInfo, error) {
	acct, err := c.newSpot(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get account: %w", err)
	}

	balances := make([]BalanceInfo, 0, len(acct.Balances))
	for _, b := range acct.Balances {
		free := parseFloat(b.Free)
		locked := parseFloat(b.Locked)
		if free > 0 || locked > 0 {
			balances = append(balances, BalanceInfo{Asset: b.Asset, Free: free, Locked: locked})
		}
	}

	return &AccountInfo{
		CanTrade:    acct.CanTrade,
		CanWithdraw: acct.CanWithdraw,
		CanDeposit:  acct.CanDeposit,
		AccountType: acct.AccountType,
		Balances:    balances,
	}, nil
}

// GetFreeBalance implements exchange.SpotBalanceFetcher.
// Returns the free (available) balance of the given asset.
func (c *Client) GetFreeBalance(ctx context.Context, creds exchange.Credentials, asset string) (decimal.Decimal, error) {
	b, err := c.GetBalance(ctx, creds, asset)
	if err != nil {
		return decimal.Zero, err
	}
	return decimal.NewFromFloat(b.Free), nil
}

// GetBalance returns the balance for a specific asset.
func (c *Client) GetBalance(ctx context.Context, creds exchange.Credentials, asset string) (*BalanceInfo, error) {
	acct, err := c.newSpot(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get balance: %w", err)
	}
	for _, b := range acct.Balances {
		if b.Asset == asset {
			return &BalanceInfo{
				Asset:  b.Asset,
				Free:   parseFloat(b.Free),
				Locked: parseFloat(b.Locked),
			}, nil
		}
	}
	return nil, fmt.Errorf("binance: asset %s not found", asset)
}

// GetExchangeAsset returns trading pair info.
func (c *Client) GetExchangeAsset(ctx context.Context, symbol string) (*ExchangeAsset, error) {
	// Public endpoint — no credentials needed.
	info, err := c.newSpot(exchange.Credentials{}).NewExchangeInfoService().Symbol(symbol).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance exchange info: %w", err)
	}
	for _, s := range info.Symbols {
		if s.Symbol == symbol {
			return &ExchangeAsset{
				Symbol:     s.Symbol,
				BaseAsset:  s.BaseAsset,
				QuoteAsset: s.QuoteAsset,
				Status:     s.Status,
				Tradable:   s.IsSpotTradingAllowed,
			}, nil
		}
	}
	return nil, fmt.Errorf("binance: symbol %s not found", symbol)
}

// GetServerTime returns Binance server time in Unix milliseconds.
func (c *Client) GetServerTime(ctx context.Context) (int64, error) {
	resp, err := c.newSpot(exchange.Credentials{}).NewServerTimeService().Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("binance server time: %w", err)
	}
	return resp, nil
}
