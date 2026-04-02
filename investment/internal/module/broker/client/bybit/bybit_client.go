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
	"strings"
	"time"

	"mallow/investment/internal/module/broker/client"
)

const bybitBaseURL = "https://api.bybit.com"

// Client implements broker.BrokerClient for Bybit V5 API.
type Client struct {
	http *http.Client
}

func NewClient() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

// sign creates HMAC-SHA256 signature for Bybit V5.
func sign(apiSecret, timestamp, apiKey, recvWindow, payload string) string {
	raw := timestamp + apiKey + recvWindow + payload
	h := hmac.New(sha256.New, []byte(apiSecret))
	h.Write([]byte(raw))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *Client) get(ctx context.Context, path string, params map[string]string, apiKey, apiSecret string) ([]byte, error) {
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
	sig := sign(apiSecret, ts, apiKey, recvWindow, payload)

	fullURL := bybitBaseURL + path
	if payload != "" {
		fullURL += "?" + payload
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-BAPI-API-KEY", apiKey)
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

func (c *Client) Authenticate(ctx context.Context, creds client.Credentials) (*client.AuthResponse, error) {
	body, err := c.get(ctx, "/v5/user/query-api", nil, creds.APIKey, creds.APISecret)
	if err != nil {
		return nil, fmt.Errorf("bybit authentication failed: %w", err)
	}
	var resp struct {
		RetCode int `json:"retCode"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit authentication failed: retCode=%d", resp.RetCode)
	}
	return &client.AuthResponse{
		AccessToken: creds.APIKey + ":" + creds.APISecret,
		ExpiresAt:   time.Now().Add(100 * 365 * 24 * time.Hour),
	}, nil
}

func (c *Client) RefreshToken(_ context.Context, _ string) (*client.AuthResponse, error) {
	return &client.AuthResponse{ExpiresAt: time.Now().Add(100 * 365 * 24 * time.Hour)}, nil
}

func (c *Client) GetPortfolio(ctx context.Context, accessToken string) (*client.Portfolio, error) {
	apiKey, apiSecret, err := splitToken(accessToken)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, "/v5/account/wallet-balance",
		map[string]string{"accountType": "UNIFIED"}, apiKey, apiSecret)
	if err != nil {
		return nil, fmt.Errorf("bybit get wallet failed: %w", err)
	}

	var resp struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				TotalEquity        string `json:"totalEquity"`
				TotalWalletBalance string `json:"totalWalletBalance"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("bybit parse wallet failed: %w", err)
	}

	var total float64
	if len(resp.Result.List) > 0 {
		fmt.Sscanf(resp.Result.List[0].TotalEquity, "%f", &total)
	}
	return &client.Portfolio{TotalValue: total, Currency: "USDT"}, nil
}

func (c *Client) GetPositions(ctx context.Context, accessToken string) ([]client.Position, error) {
	apiKey, apiSecret, err := splitToken(accessToken)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, "/v5/position/list",
		map[string]string{"category": "linear", "limit": "200"}, apiKey, apiSecret)
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
		var qty, avgPx, markPx, pnl float64
		fmt.Sscanf(p.Size, "%f", &qty)
		if qty == 0 {
			continue
		}
		fmt.Sscanf(p.AvgPrice, "%f", &avgPx)
		fmt.Sscanf(p.MarkPrice, "%f", &markPx)
		fmt.Sscanf(p.UnrealisedPnl, "%f", &pnl)
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

func (c *Client) GetTransactions(ctx context.Context, accessToken string, from, to time.Time) ([]client.Transaction, error) {
	apiKey, apiSecret, err := splitToken(accessToken)
	if err != nil {
		return nil, err
	}
	body, err := c.get(ctx, "/v5/execution/list",
		map[string]string{
			"category":  "linear",
			"startTime": fmt.Sprintf("%d", from.UnixMilli()),
			"endTime":   fmt.Sprintf("%d", to.UnixMilli()),
			"limit":     "100",
		}, apiKey, apiSecret)
	if err != nil {
		return nil, fmt.Errorf("bybit get executions failed: %w", err)
	}

	var resp struct {
		Result struct {
			List []struct {
				ExecId    string `json:"execId"`
				Symbol    string `json:"symbol"`
				Side      string `json:"side"`
				ExecQty   string `json:"execQty"`
				ExecPrice string `json:"execPrice"`
				ExecTime  string `json:"execTime"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	var txns []client.Transaction
	for _, e := range resp.Result.List {
		var qty, price float64
		var tsMs int64
		fmt.Sscanf(e.ExecQty, "%f", &qty)
		fmt.Sscanf(e.ExecPrice, "%f", &price)
		fmt.Sscanf(e.ExecTime, "%d", &tsMs)
		txns = append(txns, client.Transaction{
			ExternalID:      e.ExecId,
			Symbol:          e.Symbol,
			TransactionType: strings.ToLower(e.Side),
			Quantity:        qty,
			Price:           price,
			Amount:          qty * price,
			Currency:        "USDT",
			TransactionDate: time.UnixMilli(tsMs),
		})
	}
	return txns, nil
}

func (c *Client) GetMarketPrice(ctx context.Context, symbol string) (*client.MarketPrice, error) {
	body, err := c.get(ctx, "/v5/market/tickers",
		map[string]string{"category": "spot", "symbol": symbol}, "", "")
	if err != nil {
		return nil, fmt.Errorf("bybit get ticker failed: %w", err)
	}

	var resp struct {
		Result struct {
			List []struct {
				Symbol    string `json:"symbol"`
				LastPrice string `json:"lastPrice"`
				Volume24h string `json:"volume24h"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Result.List) == 0 {
		return nil, fmt.Errorf("bybit ticker not found for %s", symbol)
	}

	var price float64
	fmt.Sscanf(resp.Result.List[0].LastPrice, "%f", &price)
	return &client.MarketPrice{Symbol: symbol, Price: price}, nil
}

func (c *Client) GetBatchMarketPrices(ctx context.Context, symbols []string) (map[string]*client.MarketPrice, error) {
	result := make(map[string]*client.MarketPrice, len(symbols))
	for _, s := range symbols {
		mp, err := c.GetMarketPrice(ctx, s)
		if err != nil {
			continue
		}
		result[s] = mp
	}
	return result, nil
}

func splitToken(token string) (string, string, error) {
	for i, r := range token {
		if r == ':' {
			return token[:i], token[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("invalid bybit token format")
}
