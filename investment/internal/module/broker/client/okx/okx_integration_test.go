//go:build integration

package okx

import (
	"context"
	"os"
	"testing"

	"mallow/investment/internal/module/broker/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func okxSimCreds(t *testing.T) client.Credentials {
	t.Helper()
	apiKey := os.Getenv("OKX_API_KEY")
	apiSecret := os.Getenv("OKX_API_SECRET")
	passphrase := os.Getenv("OKX_PASSPHRASE")
	if apiKey == "" || apiSecret == "" || passphrase == "" {
		t.Skip("OKX_API_KEY / OKX_API_SECRET / OKX_PASSPHRASE not set")
	}
	return client.Credentials{
		APIKey:     apiKey,
		APISecret:  apiSecret,
		Passphrase: &passphrase,
		IsPaper:    true, // simulated trading → x-simulated-trading: 1
	}
}

func TestOKX_Authenticate(t *testing.T) {
	c := NewOKXClient()
	creds := okxSimCreds(t)

	resp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.AccessToken)
	t.Logf("access_token length: %d", len(resp.AccessToken))
}

func TestOKX_GetPortfolio(t *testing.T) {
	c := NewOKXClient()
	creds := okxSimCreds(t)

	authResp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)

	portfolio, err := c.GetPortfolio(context.Background(), authResp.AccessToken)
	require.NoError(t, err)
	assert.Equal(t, "USD", portfolio.Currency)
	t.Logf("total=%.4f cash=%.4f", portfolio.TotalValue, portfolio.CashBalance)
	for asset, val := range portfolio.AssetAllocation {
		t.Logf("  %s=%.4f USD", asset, val)
	}
}

func TestOKX_GetPositions(t *testing.T) {
	c := NewOKXClient()
	creds := okxSimCreds(t)

	authResp, err := c.Authenticate(context.Background(), creds)
	require.NoError(t, err)

	positions, err := c.GetPositions(context.Background(), authResp.AccessToken)
	require.NoError(t, err)
	t.Logf("positions count: %d", len(positions))
	for _, p := range positions {
		t.Logf("  %s qty=%.4f price=%.4f upl=%.4f", p.Symbol, p.Quantity, p.CurrentPrice, p.UnrealizedGain)
	}
}

func TestOKX_GetMarketPrice(t *testing.T) {
	c := NewOKXClient()
	price, err := c.GetMarketPrice(context.Background(), "BTC")
	require.NoError(t, err)
	assert.Greater(t, price.Price, 0.0)
	t.Logf("BTC-USDT price=%.2f change=%.2f%%", price.Price, price.ChangePct)
}
