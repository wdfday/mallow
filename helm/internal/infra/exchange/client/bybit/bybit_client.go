package bybit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/shopspring/decimal"
	"mallow/helm/internal/infra/exchange/client"
)

const bybitBaseURL = "https://api.bybit.com"

// Client implements broker.BrokerClient for Bybit V5 API.
type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

func sign(apiSecret, timestamp, apiKey, recvWindow, payload string) string {
	raw := timestamp + apiKey + recvWindow + payload
	h := hmac.New(sha256.New, []byte(apiSecret))
	h.Write([]byte(raw))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *Client) get(ctx context.Context, path string, params map[string]string, creds client.Credentials) ([]byte, error) {
	vals := url.Values{}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals.Set(k, params[k])
	}
	payload := vals.Encode()

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	recvWindow := "5000"
	sig := sign(creds.APISecret, ts, creds.APIKey, recvWindow, payload)

	fullURL := bybitBaseURL + path
	if payload != "" {
		fullURL += "?" + payload
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-BAPI-API-KEY", creds.APIKey)
	req.Header.Set("X-BAPI-SIGN", sig)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) Validate(ctx context.Context, creds client.Credentials) error {
	body, err := c.get(ctx, "/v5/user/query-api", nil, creds)
	if err != nil {
		return fmt.Errorf("bybit authentication failed: %w", err)
	}
	var resp struct {
		RetCode int `json:"retCode"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.RetCode != 0 {
		return fmt.Errorf("bybit authentication failed: retCode=%d", resp.RetCode)
	}
	return nil
}

func (c *Client) GetPortfolio(ctx context.Context, creds client.Credentials) (*client.Portfolio, error) {
	body, err := c.get(ctx, "/v5/account/wallet-balance",
		map[string]string{"accountType": "UNIFIED"}, creds)
	if err != nil {
		return nil, fmt.Errorf("bybit get wallet failed: %w", err)
	}
	var resp struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				TotalEquity string `json:"totalEquity"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("bybit parse wallet failed: %w", err)
	}
	var total decimal.Decimal
	if len(resp.Result.List) > 0 {
		total, _ = decimal.NewFromString(resp.Result.List[0].TotalEquity)
	}
	return &client.Portfolio{TotalValue: total, Currency: "USDT"}, nil
}

func (c *Client) GetPositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	body, err := c.get(ctx, "/v5/position/list",
		map[string]string{"category": "linear", "limit": "200"}, creds)
	if err != nil {
		return nil, fmt.Errorf("bybit get positions failed: %w", err)
	}
	var resp struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				Symbol        string `json:"symbol"`
				Size          string `json:"size"`
				AvgPrice      string `json:"avgPrice"`
				MarkPrice     string `json:"markPrice"`
				UnrealisedPnl string `json:"unrealisedPnl"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	var positions []client.Position
	for _, p := range resp.Result.List {
		qty, _ := decimal.NewFromString(p.Size)
		if !qty.IsPositive() {
			continue
		}
		avgPx, _ := decimal.NewFromString(p.AvgPrice)
		markPx, _ := decimal.NewFromString(p.MarkPrice)
		pnl, _ := decimal.NewFromString(p.UnrealisedPnl)
		positions = append(positions, client.Position{
			Symbol:             p.Symbol,
			Quantity:           qty,
			AverageCostPerUnit: avgPx,
			CurrentPrice:       markPx,
			UnrealizedGain:     pnl,
			Currency:           "USDT",
		})
	}
	return positions, nil
}
