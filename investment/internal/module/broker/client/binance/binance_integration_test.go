//go:build integration

package binance

import (
	"context"
	"os"
	"testing"

	"mallow/investment/internal/module/broker/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func binanceDemoCreds(t *testing.T) client.Credentials {
	t.Helper()
	apiKey := os.Getenv("BINANCE_API_KEY")
	apiSecret := os.Getenv("BINANCE_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		t.Skip("BINANCE_API_KEY / BINANCE_API_SECRET not set")
	}
	return client.Credentials{APIKey: apiKey, APISecret: apiSecret, IsPaper: true}
}

func TestBinance_Authenticate(t *testing.T) {
	c := NewClient()
	creds := binanceDemoCreds(t)

	resp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.AccessToken)
	t.Logf("access_token: %s", resp.AccessToken)
}

func TestBinance_GetPortfolio(t *testing.T) {
	c := NewClient()
	creds := binanceDemoCreds(t)

	authResp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)

	portfolio, err := c.GetPortfolio(context.Background(), authResp.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, "USDT", portfolio.Currency)
	t.Logf("total=%.4f cash=%.4f", portfolio.TotalValue, portfolio.CashBalance)
}

func TestBinance_GetPositions(t *testing.T) {
	c := NewClient()
	creds := binanceDemoCreds(t)

	authResp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)

	positions, err := c.GetPositions(context.Background(), authResp.AccessToken)
	require.NoError(t, err)
	t.Logf("positions count: %d", len(positions))
	for _, p := range positions {
		t.Logf("  %s qty=%.8f", p.Symbol, p.Quantity)
	}
}

func TestBinance_GetMarketPrice(t *testing.T) {
	c := NewClient()
	price, err := c.GetMarketPrice(context.Background(), "BTCUSDT")
	require.NoError(t, err)
	assert.Greater(t, price.Price, 0.0)
	t.Logf("BTCUSDT price=%.2f", price.Price)
}
