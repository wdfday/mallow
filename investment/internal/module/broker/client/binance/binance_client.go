package binance

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"mallow/investment/internal/module/broker/client"

	binancesdk "github.com/adshao/go-binance/v2"
)

const demoBinanceURL = "https://demo-api.binance.com"

// Client implements broker.BrokerClient for Binance.
type Client struct{}

func NewClient() *Client { return &Client{} }

func newSDK(creds client.Credentials) *binancesdk.Client {
	c := binancesdk.NewClient(creds.APIKey, creds.APISecret)
	if creds.IsPaper {
		c.BaseURL = demoBinanceURL
	}
	return c
}

func (c *Client) Validate(_ context.Context, creds client.Credentials) error {
	if _, err := newSDK(creds).NewGetAccountService().Do(context.Background()); err != nil {
		return fmt.Errorf("binance authentication failed: %w", err)
	}
	return nil
}

func (c *Client) GetPortfolio(ctx context.Context, creds client.Credentials) (*client.Portfolio, error) {
	account, err := newSDK(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get account failed: %w", err)
	}
	var total, cash decimal.Decimal
	for _, b := range account.Balances {
		free, _ := decimal.NewFromString(b.Free)
		locked, _ := decimal.NewFromString(b.Locked)
		qty := free.Add(locked)
		total = total.Add(qty)
		if b.Asset == "USDT" || b.Asset == "USDC" || b.Asset == "BUSD" {
			cash = cash.Add(qty)
		}
	}
	return &client.Portfolio{TotalValue: total, Currency: "USDT", CashBalance: cash}, nil
}

func (c *Client) GetPositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	account, err := newSDK(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get positions failed: %w", err)
	}
	var positions []client.Position
	for _, b := range account.Balances {
		free, _ := decimal.NewFromString(b.Free)
		locked, _ := decimal.NewFromString(b.Locked)
		qty := free.Add(locked)
		if !qty.IsPositive() {
			continue
		}
		positions = append(positions, client.Position{Symbol: b.Asset, Quantity: qty, Currency: "USDT"})
	}
	return positions, nil
}

func (c *Client) GetTransactions(_ context.Context, _ client.Credentials, _, _ time.Time) ([]client.Transaction, error) {
	return nil, nil
}

func (c *Client) GetMarketPrice(ctx context.Context, symbol string) (*client.MarketPrice, error) {
	svc := binancesdk.NewClient("", "")
	prices, err := svc.NewListPricesService().Symbol(symbol).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get price failed: %w", err)
	}
	if len(prices) == 0 {
		return nil, fmt.Errorf("no price found for %s", symbol)
	}
	price, _ := decimal.NewFromString(prices[0].Price)
	return &client.MarketPrice{Symbol: symbol, Price: price}, nil
}

func (c *Client) GetBatchMarketPrices(ctx context.Context, symbols []string) (map[string]*client.MarketPrice, error) {
	svc := binancesdk.NewClient("", "")
	all, err := svc.NewListPricesService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get prices failed: %w", err)
	}
	want := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		want[s] = true
	}
	result := make(map[string]*client.MarketPrice)
	for _, p := range all {
		if want[p.Symbol] {
			price, _ := decimal.NewFromString(p.Price)
			result[p.Symbol] = &client.MarketPrice{Symbol: p.Symbol, Price: price}
		}
	}
	return result, nil
}
