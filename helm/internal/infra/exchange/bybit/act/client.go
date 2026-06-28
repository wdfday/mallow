package act

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// Config holds Bybit process-level settings (no credentials).
type Config struct {
	BaseURL string // default depends on Paper flag
	Paper   bool
}

// Client is a stateless Bybit V5 REST client.
// One instance is shared across all Bybit accounts; credentials are passed per-call.
type Client struct {
	baseURL string
	paper   bool
	client  *http.Client // shared connection pool
}

// New creates a shared stateless Bybit client.
func New(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		if cfg.Paper {
			baseURL = "https://api-demo.bybit.com"
		} else {
			baseURL = "https://api.bybit.com"
		}
	}
	return &Client{
		baseURL: baseURL,
		paper:   cfg.Paper,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

var (
	_ exchange.Exchange           = (*Client)(nil)
	_ exchange.ExitOrderPlacer    = (*Client)(nil)
	_ exchange.LeverageSetter     = (*Client)(nil)
	_ exchange.AccountStreamer    = (*Client)(nil)
	_ exchange.HistoryFetcher     = (*Client)(nil)
	_ exchange.SymbolInfoProvider = (*Client)(nil)
)

func (c *Client) Name() string { return "bybit" }

// doSigned performs a signed HTTP request to Bybit V5 API using the given credentials.
func (c *Client) doSigned(ctx context.Context, creds exchange.Credentials, method, path string, body any, out any) error {
	return c.doSignedAt(ctx, creds, c.baseURL, method, path, body, out)
}

// doSignedAt is the underlying signed HTTP request; baseURL overrides c.baseURL.
// Used by broker methods to honour per-call IsPaper without reconstructing the client.
func (c *Client) doSignedAt(ctx context.Context, creds exchange.Credentials, baseURL string, method, path string, body any, out any) error {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"

	payload, reqURL := buildRequestAt(baseURL, method, path, body)

	preSign := timestamp + creds.APIKey + recvWindow + string(payload)
	signature := sign(preSign, creds.APISecret)

	var bodyReader io.Reader
	if method != http.MethodGet && payload != nil {
		bodyReader = strings.NewReader(string(payload))
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return fmt.Errorf("bybit: build request %s %s: %w", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", creds.APIKey)
	req.Header.Set("X-BAPI-SIGN", signature)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("bybit: http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("bybit: read response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return json.Unmarshal(respBody, out)
}

func (c *Client) buildRequest(method, path string, body any) (payload []byte, reqURL string) {
	return buildRequestAt(c.baseURL, method, path, body)
}

func buildRequestAt(baseURL, method, path string, body any) (payload []byte, reqURL string) {
	if method == http.MethodGet {
		if m, ok := body.(map[string]string); ok {
			params := make([]string, 0, len(m))
			for k, v := range m {
				params = append(params, k+"="+v)
			}
			qs := strings.Join(params, "&")
			return []byte(qs), baseURL + path + "?" + qs
		}
		return nil, baseURL + path
	}
	data, _ := json.Marshal(body)
	return data, baseURL + path
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// doPublic performs an unsigned GET to a Bybit public endpoint (no credentials required).
func (c *Client) doPublic(ctx context.Context, path string, params map[string]string, out any) error {
	qs := ""
	if len(params) > 0 {
		parts := make([]string, 0, len(params))
		for k, v := range params {
			parts = append(parts, k+"="+v)
		}
		qs = "?" + strings.Join(parts, "&")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+qs, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

// GetSymbolFilters implements exchange.SymbolInfoProvider.
// Calls /v5/market/instruments-info (public) to fetch lotSizeFilter.basePrecision
// (QtyStep) and priceFilter.tickSize (PriceTick) for the given spot symbol.
func (c *Client) GetSymbolFilters(ctx context.Context, symbol string) (exchange.SymbolFilters, error) {
	// Try "spot" first; fall back to "linear" for perpetual contracts.
	for _, cat := range []string{"spot", "linear"} {
		var resp struct {
			RetCode int    `json:"retCode"`
			RetMsg  string `json:"retMsg"`
			Result  struct {
				List []struct {
					Symbol        string `json:"symbol"`
					BaseCoin      string `json:"baseCoin"`
					QuoteCoin     string `json:"quoteCoin"`
					LotSizeFilter struct {
						BasePrecision string `json:"basePrecision"`
						MinOrderQty   string `json:"minOrderQty"`
					} `json:"lotSizeFilter"`
					PriceFilter struct {
						TickSize string `json:"tickSize"`
					} `json:"priceFilter"`
				} `json:"list"`
			} `json:"result"`
		}
		if err := c.doPublic(ctx, "/v5/market/instruments-info", map[string]string{
			"category": cat,
			"symbol":   symbol,
		}, &resp); err != nil {
			continue
		}
		if resp.RetCode != 0 || len(resp.Result.List) == 0 {
			continue
		}
		item := resp.Result.List[0]
		f := exchange.SymbolFilters{
			BaseAsset:  item.BaseCoin,
			QuoteAsset: item.QuoteCoin,
		}
		if s := item.LotSizeFilter.BasePrecision; s != "" {
			f.QtyStep, _ = decimal.NewFromString(s)
		}
		if s := item.LotSizeFilter.MinOrderQty; s != "" {
			f.MinQty, _ = decimal.NewFromString(s)
		}
		if s := item.PriceFilter.TickSize; s != "" {
			f.PriceTick, _ = decimal.NewFromString(s)
		}
		return f, nil
	}
	return exchange.SymbolFilters{}, fmt.Errorf("bybit: instrument %s not found", symbol)
}
