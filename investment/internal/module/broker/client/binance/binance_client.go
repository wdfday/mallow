package binance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"mallow/investment/internal/module/broker/client"

	binancesdk "github.com/adshao/go-binance/v2"
)

const demoBinanceURL = "https://demo-api.binance.com"

// Client implements broker.BrokerClient for Binance.
type Client struct{}

func NewClient() *Client { return &Client{} }

func newBinanceSDK(apiKey, apiSecret string, isPaper bool) *binancesdk.Client {
	c := binancesdk.NewClient(apiKey, apiSecret)
	if isPaper {
		c.BaseURL = demoBinanceURL
	}
	return c
}

func (c *Client) Authenticate(_ context.Context, creds client.Credentials) (*client.AuthResponse, error) {
	svc := newBinanceSDK(creds.APIKey, creds.APISecret, creds.IsPaper)
	if _, err := svc.NewGetAccountService().Do(context.Background()); err != nil {
		return nil, fmt.Errorf("binance authentication failed: %w", err)
	}
	mode := "live"
	if creds.IsPaper {
		mode = "demo"
	}
	token := mode + "|" + creds.APIKey + ":" + creds.APISecret
	return &client.AuthResponse{
		AccessToken: token,
		ExpiresAt:   time.Now().Add(100 * 365 * 24 * time.Hour),
	}, nil
}

func (c *Client) RefreshToken(_ context.Context, _ string) (*client.AuthResponse, error) {
	return &client.AuthResponse{ExpiresAt: time.Now().Add(100 * 365 * 24 * time.Hour)}, nil
}

func (c *Client) GetPortfolio(ctx context.Context, accessToken string) (*client.Portfolio, error) {
	apiKey, apiSecret, isPaper, err := splitToken(accessToken)
	if err != nil {
		return nil, err
	}
	svc := newBinanceSDK(apiKey, apiSecret, isPaper)
	account, err := svc.NewGetAccountService().Do(ctx)
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

func (c *Client) GetPositions(ctx context.Context, accessToken string) ([]client.Position, error) {
	apiKey, apiSecret, isPaper, err := splitToken(accessToken)
	if err != nil {
		return nil, err
	}
	svc := newBinanceSDK(apiKey, apiSecret, isPaper)
	account, err := svc.NewGetAccountService().Do(ctx)
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

func (c *Client) GetTransactions(_ context.Context, _ string, _, _ time.Time) ([]client.Transaction, error) {
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

// splitToken parses "demo|key:secret" or "live|key:secret" or legacy "key:secret".
func splitToken(token string) (apiKey, apiSecret string, isPaper bool, err error) {
	if head, tail, ok := strings.Cut(token, "|"); ok {
		isPaper = head == "demo"
		token = tail
	}
	idx := strings.IndexByte(token, ':')
	if idx < 0 {
		return "", "", false, fmt.Errorf("invalid binance token format")
	}
	return token[:idx], token[idx+1:], isPaper, nil
}
