package oanda

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"mallow/helm/internal/infra/exchange"
)

// Config holds OANDA process-level settings (no credentials).
type Config struct {
	BaseURL string // default: https://api-fxpractice.oanda.com
}

// Client is a stateless OANDA v20 REST client.
// One instance is shared across all OANDA accounts; credentials are passed per-call.
type Client struct {
	baseURL string
	client  *http.Client
}

// New creates a shared stateless OANDA client.
func New(cfg Config) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api-fxpractice.oanda.com"
	}
	return &Client{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

var (
	_ exchange.Exchange        = (*Client)(nil)
	_ exchange.OrderReconciler = (*Client)(nil)
)

func (c *Client) Name() string { return "oanda" }

// doRequest performs an authenticated HTTP request to OANDA v20 API using the given credentials.
// creds.APIKey is the Personal Access Token; creds.AccountID is the OANDA account ID.
func (c *Client) doRequest(ctx context.Context, creds exchange.Credentials, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = strings.NewReader(string(data))
	}

	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.APIKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}
	return json.Unmarshal(respBody, out)
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}
