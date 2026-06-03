package act

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/delivery"
	"github.com/adshao/go-binance/v2/futures"

	"mallow/helm/internal/infra/exchange"
)

const paperBaseURL = "https://demo-api.binance.com"
const paperFuturesURL = "https://demo-fapi.binance.com"
const paperDeliveryURL = "https://demo-dapi.binance.com"

// Client is a stateless Binance HTTP client.
// One instance is shared across all Binance accounts; credentials are passed per-call.
type Client struct {
	httpClient *http.Client // shared connection pool
	baseURL    string       // spot base URL override
	futBaseURL string       // USDM futures base URL override
	delivURL   string       // COINM delivery base URL override
	paper      bool
	// timeOffset is (localTimeMs - binanceServerTimeMs); applied to every signed
	// request via gobinance.Client.TimeOffset. Kept as an int64 so it can be
	// read/written atomically without a mutex. Updated by SyncTime.
	timeOffset atomic.Int64
}

// New creates a shared stateless Binance client.
func New(paper bool) *Client {
	baseURL := ""
	futURL := ""
	delivURL := ""
	if paper {
		baseURL = paperBaseURL
		futURL = paperFuturesURL
		delivURL = paperDeliveryURL
	}
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		baseURL:    baseURL,
		futBaseURL: futURL,
		delivURL:   delivURL,
		paper:      paper,
	}
}

var (
	_ exchange.Exchange           = (*Client)(nil)
	_ exchange.ExitOrderPlacer    = (*Client)(nil)
	_ exchange.LeverageSetter     = (*Client)(nil)
	_ exchange.AccountSyncer      = (*Client)(nil)
	_ exchange.AccountStreamer    = (*Client)(nil)
	_ exchange.HistoryFetcher     = (*Client)(nil)
	_ exchange.PriceFetcher       = (*Client)(nil)
	_ exchange.SymbolInfoProvider = (*Client)(nil)
	_ exchange.TimeSyncer         = (*Client)(nil)
)

func (c *Client) Name() string { return "binance" }

// SyncTime fetches Binance server time (public endpoint, no credentials) and
// stores the local-vs-server offset so every subsequent signed request sends
// the correct timestamp. Call this once at startup and then periodically
// (every few minutes) to guard against slow clock drift.
//
//	offset = localTimeMs - serverTimeMs
//	signed timestamp = currentTimestamp() - offset ≈ serverTime
//
// When the local clock is behind the server (offset < 0), the timestamp is
// bumped forward; when ahead (offset > 0), it is nudged back. Either way the
// request timestamp stays within Binance's recvWindow.
func (c *Client) SyncTime(ctx context.Context) error {
	serverMs, err := c.GetServerTime(ctx)
	if err != nil {
		return err
	}
	localMs := time.Now().UnixMilli()
	offset := localMs - serverMs
	c.timeOffset.Store(offset)
	slog.Info("binance: server time synced",
		"server_ms", serverMs, "local_ms", localMs, "offset_ms", offset)
	return nil
}

// newSpot creates a gobinance.Client with the given credentials, reusing the shared HTTP pool.
func (c *Client) newSpot(creds exchange.Credentials) *gobinance.Client {
	cl := gobinance.NewClient(creds.APIKey, creds.APISecret)
	cl.HTTPClient = c.httpClient
	if c.baseURL != "" {
		cl.BaseURL = c.baseURL
	}
	cl.TimeOffset = c.timeOffset.Load()
	return cl
}

// newFut creates a USDM-margined futures.Client (FAPI), reusing the shared HTTP pool.
func (c *Client) newFut(creds exchange.Credentials) *futures.Client {
	cl := futures.NewClient(creds.APIKey, creds.APISecret)
	cl.HTTPClient = c.httpClient
	if c.futBaseURL != "" {
		cl.BaseURL = c.futBaseURL
	}
	cl.TimeOffset = c.timeOffset.Load()
	return cl
}

// newDelivery creates a COIN-M-margined delivery.Client (DAPI), reusing the shared HTTP pool.
func (c *Client) newDelivery(creds exchange.Credentials) *delivery.Client {
	cl := delivery.NewClient(creds.APIKey, creds.APISecret)
	cl.HTTPClient = c.httpClient
	if c.delivURL != "" {
		cl.BaseURL = c.delivURL
	}
	cl.TimeOffset = c.timeOffset.Load()
	return cl
}
