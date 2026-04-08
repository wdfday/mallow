package action

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
)

// Config holds Bybit API credentials.
type Config struct {
	APIKey    string
	APISecret string
	BaseURL   string // default: https://api.bybit.com
	Testnet   bool   // true → https://api-testnet.bybit.com
}

// Client wraps the Bybit V5 REST API.
type Client struct {
	cfg    Config
	client *http.Client
}

// New creates a new Bybit action client.
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		if cfg.Testnet {
			cfg.BaseURL = "https://api-testnet.bybit.com"
		} else {
			cfg.BaseURL = "https://api.bybit.com"
		}
	}
	return &Client{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) Name() string { return "bybit" }

// doSigned performs a signed HTTP request to Bybit V5 API.
func (c *Client) doSigned(ctx context.Context, method, path string, body any, out any) error {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	recvWindow := "5000"

	payload, reqURL := c.buildRequest(method, path, body)

	preSign := timestamp + c.cfg.APIKey + recvWindow + string(payload)
	signature := sign(preSign, c.cfg.APISecret)

	var bodyReader io.Reader
	if method != http.MethodGet && payload != nil {
		bodyReader = strings.NewReader(string(payload))
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", c.cfg.APIKey)
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
			return []byte(qs), c.cfg.BaseURL + path + "?" + qs
		}
		return nil, c.cfg.BaseURL + path
	}
	data, _ := json.Marshal(body)
	return data, c.cfg.BaseURL + path
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func parseDecimal(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

// parseFloat is kept for local display types that remain float64.
func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// apiResponse is the common Bybit V5 API response envelope.
type apiResponse[T any] struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  T      `json:"result"`
}
