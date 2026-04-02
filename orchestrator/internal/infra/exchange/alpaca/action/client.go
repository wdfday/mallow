package action

import (
	"strings"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
)

// Config holds Alpaca API credentials.
type Config struct {
	APIKey    string
	APISecret string
	BaseURL   string // default: https://paper-api.alpaca.markets
}

// Client implements exchange.Exchange and optional interfaces (AccountSyncer,
// PriceFetcher, AccountStreamer) for a single Alpaca account.
type Client struct {
	sdk *alpacasdk.Client
	md  *marketdata.Client
}

// New creates a per-account Alpaca action client.
func New(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://paper-api.alpaca.markets"
	}
	sdk := alpacasdk.NewClient(alpacasdk.ClientOpts{
		APIKey:    cfg.APIKey,
		APISecret: cfg.APISecret,
		BaseURL:   baseURL,
	})
	md := marketdata.NewClient(marketdata.ClientOpts{
		APIKey:    cfg.APIKey,
		APISecret: cfg.APISecret,
	})
	return &Client{sdk: sdk, md: md}
}

func (c *Client) Name() string { return "alpaca" }

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
