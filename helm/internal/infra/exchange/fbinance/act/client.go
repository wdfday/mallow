package act

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/adshao/go-binance/v2/futures"

	"mallow/helm/internal/infra/exchange"
)

const paperFuturesURL = "https://demo-fapi.binance.com"

// Client is a stateless Binance USDM-futures HTTP client.
// One instance is shared across all Binance USDM futures accounts; credentials are passed per-call.
type Client struct {
	httpClient *http.Client
	futBaseURL string
	paper      bool
	// timeOffset is (localTimeMs - binanceServerTimeMs); applied to every signed
	// request via futures.Client.TimeOffset. Updated by SyncTime.
	timeOffset atomic.Int64
}

// New creates a shared stateless Binance USDM futures client.
func New(paper bool) *Client {
	futURL := ""
	if paper {
		futURL = paperFuturesURL
	}
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		futBaseURL: futURL,
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

func (c *Client) Name() string { return "fbinance" }

// SyncTime fetches Binance FAPI server time and stores the offset.
func (c *Client) SyncTime(ctx context.Context) error {
	serverMs, err := c.GetServerTime(ctx)
	if err != nil {
		return err
	}
	localMs := time.Now().UnixMilli()
	offset := localMs - serverMs
	c.timeOffset.Store(offset)
	slog.Info("fbinance: server time synced",
		"server_ms", serverMs, "local_ms", localMs, "offset_ms", offset)
	return nil
}

// GetServerTime returns Binance FAPI server time in Unix milliseconds.
func (c *Client) GetServerTime(ctx context.Context) (int64, error) {
	resp, err := c.newFut(exchange.Credentials{}).NewServerTimeService().Do(ctx)
	if err != nil {
		return 0, fmt.Errorf("fbinance server time: %w", err)
	}
	return resp, nil
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
