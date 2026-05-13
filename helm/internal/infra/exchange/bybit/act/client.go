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
			baseURL = "https://api-testnet.bybit.com"
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

func (c *Client) Name() string { return "bybit" }

// doSigned performs a signed HTTP request to Bybit V5 API using the given credentials.
func (c *Client) doSigned(ctx context.Context, creds exchange.Credentials, method, path string, body any, out any) error {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"

	payload, reqURL := c.buildRequest(method, path, body)

	preSign := timestamp + creds.APIKey + recvWindow + string(payload)
	signature := sign(preSign, creds.APISecret)

	var bodyReader io.Reader
	if method != http.MethodGet && payload != nil {
		bodyReader = strings.NewReader(string(payload))
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", creds.APIKey)
	req.Header.Set("X-BAPI-SIGN", signature)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)

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

func (c *Client) buildRequest(method, path string, body any) (payload []byte, reqURL string) {
	if method == http.MethodGet {
		if m, ok := body.(map[string]string); ok {
			params := make([]string, 0, len(m))
			for k, v := range m {
				params = append(params, k+"="+v)
			}
			qs := strings.Join(params, "&")
			return []byte(qs), c.baseURL + path + "?" + qs
		}
		return nil, c.baseURL + path
	}
	data, _ := json.Marshal(body)
	return data, c.baseURL + path
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
