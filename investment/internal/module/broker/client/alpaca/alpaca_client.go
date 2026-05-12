package alpaca

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"mallow/investment/internal/module/broker/client"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
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

func (c *Client) GetTransactions(_ context.Context, creds client.Credentials, from, to time.Time) ([]client.Transaction, error) {
	activities, err := newSDK(creds).GetAccountActivities(alpacasdk.GetAccountActivitiesRequest{
		ActivityTypes: []string{"FILL"},
		After:         from,
		Until:         to,
	})
	if err != nil {
		return nil, fmt.Errorf("alpaca get activities failed: %w", err)
	}
	txns := make([]client.Transaction, 0, len(activities))
	for _, a := range activities {
		txns = append(txns, client.Transaction{
			ExternalID:      a.ID,
			Symbol:          string(a.Symbol),
			TransactionType: string(a.Side),
			Amount:          a.Qty.Mul(a.Price),
			Quantity:        a.Qty,
			Price:           a.Price,
			Currency:        "USD",
			TransactionDate: a.TransactionTime,
		})
	}
	return txns, nil
}

func (c *Client) GetMarketPrice(_ context.Context, symbol string) (*client.MarketPrice, error) {
	mc := marketdata.NewClient(marketdata.ClientOpts{})
	snapshot, err := mc.GetSnapshot(symbol, marketdata.GetSnapshotRequest{})
	if err != nil {
		return nil, fmt.Errorf("alpaca get snapshot failed: %w", err)
	}
	return &client.MarketPrice{Symbol: symbol, Price: decimal.NewFromFloat(snapshot.LatestTrade.Price)}, nil
}

func (c *Client) GetBatchMarketPrices(_ context.Context, symbols []string) (map[string]*client.MarketPrice, error) {
	mc := marketdata.NewClient(marketdata.ClientOpts{})
	snapshots, err := mc.GetSnapshots(symbols, marketdata.GetSnapshotRequest{})
	if err != nil {
		return nil, fmt.Errorf("alpaca get snapshots failed: %w", err)
	}
	result := make(map[string]*client.MarketPrice, len(snapshots))
	for sym, snap := range snapshots {
		result[sym] = &client.MarketPrice{Symbol: sym, Price: decimal.NewFromFloat(snap.LatestTrade.Price)}
	}
	return result, nil
}
