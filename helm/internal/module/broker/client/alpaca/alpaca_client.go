package alpaca

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"
	"mallow/helm/internal/module/broker/client"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
)

// Client implements broker.BrokerClient for Alpaca.
type Client struct{}

func NewClient() *Client { return &Client{} }

const (
	liveBaseURL  = "https://api.alpaca.markets"
	paperBaseURL = "https://paper-api.alpaca.markets"
)

func newSDK(creds client.Credentials) *alpacasdk.Client {
	opts := alpacasdk.ClientOpts{APIKey: creds.APIKey, APISecret: creds.APISecret}
	if creds.IsPaper {
		opts.BaseURL = paperBaseURL
	} else {
		opts.BaseURL = liveBaseURL
	}
	return alpacasdk.NewClient(opts)
}

func (c *Client) Validate(_ context.Context, creds client.Credentials) error {
	if _, err := newSDK(creds).GetAccount(); err != nil {
		return fmt.Errorf("alpaca authentication failed: %w", err)
	}
	return nil
}

func (c *Client) GetPortfolio(_ context.Context, creds client.Credentials) (*client.Portfolio, error) {
	account, err := newSDK(creds).GetAccount()
	if err != nil {
		return nil, fmt.Errorf("alpaca get account failed: %w", err)
	}
	return &client.Portfolio{TotalValue: account.Equity, Currency: "USD", CashBalance: account.Cash}, nil
}

func (c *Client) GetPositions(_ context.Context, creds client.Credentials) ([]client.Position, error) {
	positions, err := newSDK(creds).GetPositions()
	if err != nil {
		return nil, fmt.Errorf("alpaca get positions failed: %w", err)
	}
	result := make([]client.Position, 0, len(positions))
	for _, p := range positions {
		curPrice := decimal.Zero
		if p.CurrentPrice != nil {
			curPrice = *p.CurrentPrice
		}
		result = append(result, client.Position{
			Symbol:       string(p.Symbol),
			Quantity:     p.Qty,
			CurrentPrice: curPrice,
			Currency:     "USD",
		})
	}
	return result, nil
}
