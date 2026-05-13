package act

import (
	"net/http"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"

	"mallow/helm/internal/infra/exchange"
)

const paperBaseURL = "https://demo-api.binance.com"
const paperFuturesURL = "https://demo-fapi.binance.com"

// Client is a stateless Binance HTTP client.
// One instance is shared across all Binance accounts; credentials are passed per-call.
type Client struct {
	httpClient *http.Client // shared connection pool
	baseURL    string       // spot base URL override
	futBaseURL string       // futures base URL override
	paper      bool
}

// New creates a shared stateless Binance client.
func New(paper bool) *Client {
	baseURL := ""
	futURL := ""
	if paper {
		baseURL = paperBaseURL
		futURL = paperFuturesURL
	}
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    baseURL,
		futBaseURL: futURL,
		paper:      paper,
	}
}

func (c *Client) Name() string { return "binance" }

// newSpot creates a gobinance.Client with the given credentials, reusing the shared HTTP pool.
func (c *Client) newSpot(creds exchange.Credentials) *gobinance.Client {
	cl := gobinance.NewClient(creds.APIKey, creds.APISecret)
	cl.HTTPClient = c.httpClient
	if c.baseURL != "" {
		cl.BaseURL = c.baseURL
	}
	return cl
}

// newFut creates a futures.Client with the given credentials, reusing the shared HTTP pool.
func (c *Client) newFut(creds exchange.Credentials) *futures.Client {
	cl := futures.NewClient(creds.APIKey, creds.APISecret)
	cl.HTTPClient = c.httpClient
	if c.futBaseURL != "" {
		cl.BaseURL = c.futBaseURL
	}
	return cl
}
