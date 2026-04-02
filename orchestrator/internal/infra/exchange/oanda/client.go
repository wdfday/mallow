package oanda

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config holds OANDA API credentials.
type Config struct {
	Token     string // Personal Access Token (Bearer)
	AccountID string // OANDA account ID (e.g. "101-001-12345-001")
	BaseURL   string // default: https://api-fxpractice.oanda.com
}

// Client implements exchange.Exchange for OANDA v20 REST API.
type Client struct {
	cfg    Config
	client *http.Client
}

// New creates a new OANDA exchange client.
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api-fxpractice.oanda.com"
	}
	return &Client{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) Name() string { return "oanda" }

// doRequest performs an authenticated HTTP request to OANDA v20 API.
func (c *Client) doRequest(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = strings.NewReader(string(data))
	}

	url := c.cfg.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)

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
