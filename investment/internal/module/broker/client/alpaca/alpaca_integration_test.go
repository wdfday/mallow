//go:build integration

package alpaca

import (
	"context"
	"os"
	"testing"
	"time"

	"mallow/investment/internal/module/broker/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func alpacaPaperCreds(t *testing.T) client.Credentials {
	t.Helper()
	apiKey := os.Getenv("ALPACA_API_KEY")
	apiSecret := os.Getenv("ALPACA_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		t.Skip("ALPACA_API_KEY / ALPACA_API_SECRET not set")
	}
	return client.Credentials{APIKey: apiKey, APISecret: apiSecret, IsPaper: true}
}

func TestAlpaca_Authenticate(t *testing.T) {
	c := NewClient()
	creds := alpacaPaperCreds(t)

	resp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.AccessToken)
	assert.True(t, resp.ExpiresAt.After(time.Now()))
	t.Logf("access_token: %s", resp.AccessToken)
}

func TestAlpaca_GetPortfolio(t *testing.T) {
	c := NewClient()
	creds := alpacaPaperCreds(t)

	authResp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)

	portfolio, err := c.GetPortfolio(context.Background(), authResp.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, "USD", portfolio.Currency)
	t.Logf("total=%.2f cash=%.2f", portfolio.TotalValue, portfolio.CashBalance)
}

func TestAlpaca_GetPositions(t *testing.T) {
	c := NewClient()
	creds := alpacaPaperCreds(t)

	authResp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)

	positions, err := c.GetPositions(context.Background(), authResp.AccessToken)
	require.NoError(t, err)
	t.Logf("positions count: %d", len(positions))
	for _, p := range positions {
		t.Logf("  %s qty=%.4f price=%.2f", p.Symbol, p.Quantity, p.CurrentPrice)
	}
}

func TestAlpaca_GetTransactions(t *testing.T) {
	c := NewClient()
	creds := alpacaPaperCreds(t)

	authResp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)

	from := time.Now().AddDate(0, -1, 0)
	to := time.Now()
	txns, err := c.GetTransactions(context.Background(), authResp.AccessToken, from, to)
	require.NoError(t, err)
	t.Logf("transactions count: %d", len(txns))
}

func TestAlpaca_GetMarketPrice(t *testing.T) {
	// Market data SDK reads credentials from env vars.
	creds := alpacaPaperCreds(t)
	t.Setenv("APCA_API_KEY_ID", creds.APIKey)
	t.Setenv("APCA_API_SECRET_KEY", creds.APISecret)

	c := NewClient()
	price, err := c.GetMarketPrice(context.Background(), "AAPL")
	require.NoError(t, err)
	assert.Greater(t, price.Price, 0.0)
	t.Logf("AAPL price=%.2f", price.Price)
}
