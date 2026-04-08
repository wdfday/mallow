package alpaca

import (
	"context"
	"fmt"
	"strings"
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

func newSDK(token string) (*alpacasdk.Client, error) {
	apiKey, apiSecret, isPaper, err := splitToken(token)
	if err != nil {
		return nil, err
	}
	return newSDKWithMode(apiKey, apiSecret, isPaper), nil
}

func newSDKWithMode(apiKey, apiSecret string, isPaper bool) *alpacasdk.Client {
	opts := alpacasdk.ClientOpts{APIKey: apiKey, APISecret: apiSecret}
	if isPaper {
		opts.BaseURL = paperBaseURL
	} else {
		opts.BaseURL = liveBaseURL
	}
	return alpacasdk.NewClient(opts)
}

func (c *Client) Authenticate(_ context.Context, creds client.Credentials) (*client.AuthResponse, error) {
	ac := newSDKWithMode(creds.APIKey, creds.APISecret, creds.IsPaper)
	if _, err := ac.GetAccount(); err != nil {
		return nil, fmt.Errorf("alpaca authentication failed: %w", err)
	}
	return &client.AuthResponse{
		AccessToken: encodeToken(creds.APIKey, creds.APISecret, creds.IsPaper),
		ExpiresAt:   time.Now().Add(100 * 365 * 24 * time.Hour),
	}, nil
}

func (c *Client) RefreshToken(_ context.Context, _ string) (*client.AuthResponse, error) {
	return &client.AuthResponse{ExpiresAt: time.Now().Add(100 * 365 * 24 * time.Hour)}, nil
}

func (c *Client) GetPortfolio(_ context.Context, accessToken string) (*client.Portfolio, error) {
	ac, err := newSDK(accessToken)
	if err != nil {
		return nil, err
	}
	account, err := ac.GetAccount()
	if err != nil {
		return nil, fmt.Errorf("alpaca get account failed: %w", err)
	}
	return &client.Portfolio{TotalValue: account.Equity, Currency: "USD", CashBalance: account.Cash}, nil
}

func (c *Client) GetPositions(_ context.Context, accessToken string) ([]client.Position, error) {
	ac, err := newSDK(accessToken)
	if err != nil {
		return nil, err
	}
	positions, err := ac.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("alpaca get positions failed: %w", err)
	}
	var result []client.Position
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

func (c *Client) GetTransactions(_ context.Context, accessToken string, from, to time.Time) ([]client.Transaction, error) {
	ac, err := newSDK(accessToken)
	if err != nil {
		return nil, err
	}
	activities, err := ac.GetAccountActivities(alpacasdk.GetAccountActivitiesRequest{
		ActivityTypes: []string{"FILL"},
		After:         from,
		Until:         to,
	})
	if err != nil {
		return nil, fmt.Errorf("alpaca get activities failed: %w", err)
	}
	var txns []client.Transaction
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

func encodeToken(apiKey, apiSecret string, isPaper bool) string {
	mode := "live"
	if isPaper {
		mode = "paper"
	}
	return mode + "|" + apiKey + ":" + apiSecret
}

func splitToken(token string) (string, string, bool, error) {
	isPaper := false
	if head, tail, ok := strings.Cut(token, "|"); ok {
		switch head {
		case "paper":
			isPaper = true
		case "live":
			isPaper = false
		default:
			return "", "", false, fmt.Errorf("invalid alpaca token mode")
		}
		token = tail
	}
	for i, r := range token {
		if r == ':' {
			return token[:i], token[i+1:], isPaper, nil
		}
	}
	return "", "", false, fmt.Errorf("invalid alpaca token format")
}
