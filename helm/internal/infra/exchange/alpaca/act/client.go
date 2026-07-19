package act

import (
	"context"
	"strings"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// Config holds Alpaca process-level settings (no credentials).
type Config struct {
	BaseURL string // default: https://paper-api.alpaca.markets
}

// Client is a stateless Alpaca client.
// One instance is shared across all Alpaca accounts; credentials are passed per-call.
type Client struct {
	baseURL string
}

// New creates a shared stateless Alpaca client.
func New(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://paper-api.alpaca.markets"
	}
	return &Client{baseURL: baseURL}
}

var (
	_ exchange.Exchange           = (*Client)(nil)
	_ exchange.ExitOrderPlacer    = (*Client)(nil)
	_ exchange.AccountSyncer      = (*Client)(nil)
	_ exchange.AccountStreamer    = (*Client)(nil)
	_ exchange.HistoryFetcher     = (*Client)(nil)
	_ exchange.SymbolInfoProvider = (*Client)(nil)
	_ exchange.ErrorClassifier    = (*Client)(nil)
)

// GetSymbolFilters implements exchange.SymbolInfoProvider.
// Alpaca stocks trade in whole shares — QtyStep=1. QuoteAsset is always "USD".
func (c *Client) GetSymbolFilters(_ context.Context, symbol string) (exchange.SymbolFilters, error) {
	return exchange.SymbolFilters{
		QtyStep:    decimal.NewFromInt(1),
		BaseAsset:  symbol,
		QuoteAsset: "USD",
	}, nil
}

func (c *Client) Name() string { return "alpaca" }

// newSDK creates an Alpaca trading SDK client with the given credentials.
func (c *Client) newSDK(creds exchange.Credentials) *alpacasdk.Client {
	return alpacasdk.NewClient(alpacasdk.ClientOpts{
		APIKey:    creds.APIKey,
		APISecret: creds.APISecret,
		BaseURL:   c.baseURL,
	})
}

// newMD creates an Alpaca market data client with the given credentials.
func (c *Client) newMD(creds exchange.Credentials) *marketdata.Client {
	return marketdata.NewClient(marketdata.ClientOpts{
		APIKey:    creds.APIKey,
		APISecret: creds.APISecret,
	})
}

// isCrypto checks if a symbol is a crypto pair (e.g. "BTC/USD").
func isCrypto(symbol string) bool {
	return strings.Contains(symbol, "/")
}

// feedFor returns the appropriate marketdata feed for a symbol type.
func feedFor(symbol string) string {
	if isCrypto(symbol) {
		return "us"
	}
	return "iex"
}
