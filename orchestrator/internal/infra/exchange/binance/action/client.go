package action

import (
	"fmt"
	"net/http"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/shopspring/decimal"
)

const testnetBaseURL = "https://demo-api.binance.com"
const testnetFuturesURL = "https://demo-fapi.binance.com"

// Config holds Binance API credentials.
type Config struct {
	APIKey    string
	APISecret string
	BaseURL   string
	Testnet   bool
}

// Client implements exchange.Exchange + SpotTrader + FuturesTrader + optional interfaces
// for a single Binance account.
// spot and fut are separate SDK clients — Binance uses different endpoints per market.
type Client struct {
	spot      *gobinance.Client
	fut       *futures.Client
	testnet   bool
	apiKey    string
	apiSecret string
}

// New creates a per-account Binance action client with both spot and futures SDK clients.
func New(cfg Config) *Client {
	spot := gobinance.NewClient(cfg.APIKey, cfg.APISecret)
	spotURL := cfg.BaseURL
	if spotURL == "" && cfg.Testnet {
		spotURL = testnetBaseURL
	}
	if spotURL != "" {
		spot.BaseURL = spotURL
	}
	spot.HTTPClient = &http.Client{Timeout: 10 * time.Second}

	fut := futures.NewClient(cfg.APIKey, cfg.APISecret)
	futURL := ""
	if cfg.Testnet {
		futURL = testnetFuturesURL
	}
	if futURL != "" {
		fut.BaseURL = futURL
	}
	fut.HTTPClient = &http.Client{Timeout: 10 * time.Second}

	return &Client{spot: spot, fut: fut, testnet: cfg.Testnet, apiKey: cfg.APIKey, apiSecret: cfg.APISecret}
}

func (c *Client) Name() string { return "binance" }

func parseDecimal(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

// parseFloat is kept for account.go display types that remain float64.
func parseFloat(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}
