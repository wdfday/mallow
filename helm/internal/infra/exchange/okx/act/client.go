package act

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"mallow/helm/internal/infra/exchange"
)

// Config holds OKX process-level settings (no credentials).
type Config struct {
	BaseURL string // default: https://www.okx.com
	Paper   bool   // true → x-simulated-trading: 1
}

// Client is a stateless OKX V5 REST client.
// One instance is shared across all OKX accounts; credentials are passed per-call.
type Client struct {
	baseURL string
	paper   bool
	client  *http.Client // shared connection pool
}

// New creates a shared stateless OKX client.
func New(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://www.okx.com"
	}
	return &Client{
		baseURL: baseURL,
		paper:   cfg.Paper,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

var (
	_ exchange.Exchange        = (*Client)(nil)
	_ exchange.ExitOrderPlacer = (*Client)(nil)
	_ exchange.LeverageSetter  = (*Client)(nil)
	_ exchange.AccountSyncer   = (*Client)(nil)
	_ exchange.AccountStreamer = (*Client)(nil)
	_ exchange.HistoryFetcher  = (*Client)(nil)
	_ exchange.PriceFetcher    = (*Client)(nil)
	_ exchange.OrderReconciler = (*Client)(nil)
)

func (c *Client) Name() string { return "okx" }

// doRequest performs a signed HTTP request to the OKX V5 API using the given credentials.
func (c *Client) doRequest(ctx context.Context, creds exchange.Credentials, method, path string, body any, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}

	url := c.baseURL + path
	var bodyReader io.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}

	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	preSign := timestamp + method + path
	if payload != nil {
		preSign += string(payload)
	}
	mac := hmac.New(sha256.New, []byte(creds.APISecret))
	mac.Write([]byte(preSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OK-ACCESS-KEY", creds.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", signature)
	req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("OK-ACCESS-PASSPHRASE", creds.Passphrase)
	if c.paper {
		req.Header.Set("x-simulated-trading", "1")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return json.Unmarshal(respBody, out)
}
