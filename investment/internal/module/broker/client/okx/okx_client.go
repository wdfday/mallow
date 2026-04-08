package okx

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
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"mallow/investment/internal/module/broker/client"
)

const (
	OKXBaseURL = "https://www.okx.com"
)

// OKXClient implements the BrokerClient interface for OKX Exchange
type OKXClient struct {
	httpClient *http.Client
	baseURL    string
}

// NewOKXClient creates a new OKX client
func NewOKXClient() *OKXClient {
	return &OKXClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL: OKXBaseURL,
	}
}

// sign creates HMAC SHA256 signature for OKX API
func (c *OKXClient) sign(timestamp, method, requestPath, body, secretKey string) string {
	message := timestamp + method + requestPath + body
	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(message))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// makeRequest makes an authenticated request to OKX API.
// isSimulated adds x-simulated-trading: 1 header for paper trading.
func (c *OKXClient) makeRequest(ctx context.Context, method, path string, body interface{}, apiKey, apiSecret, passphrase string, isSimulated bool) (*http.Response, error) {
	var bodyBytes []byte
	var err error
	bodyStr := ""

	if body != nil {
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyStr = string(bodyBytes)
	}

	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	signature := c.sign(timestamp, method, path, bodyStr, apiSecret)

	url := c.baseURL + path
	var req *http.Request
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, bytes.NewBuffer(bodyBytes))
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OK-ACCESS-KEY", apiKey)
	req.Header.Set("OK-ACCESS-SIGN", signature)
	req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("OK-ACCESS-PASSPHRASE", passphrase)
	if isSimulated {
		req.Header.Set("x-simulated-trading", "1")
	}

	return c.httpClient.Do(req)
}

// Authenticate validates OKX credentials. Set IsPaper=true for simulated trading.
func (c *OKXClient) Authenticate(ctx context.Context, credentials client.Credentials) (*client.AuthResponse, error) {
	if credentials.Passphrase == nil || *credentials.Passphrase == "" {
		return nil, fmt.Errorf("passphrase is required for OKX")
	}

	resp, err := c.makeRequest(ctx, "GET", "/api/v5/account/balance", nil,
		credentials.APIKey, credentials.APISecret, *credentials.Passphrase, credentials.IsPaper)
	if err != nil {
		return nil, fmt.Errorf("failed to validate credentials: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("authentication failed: %s", string(body))
	}

	simFlag := "0"
	if credentials.IsPaper {
		simFlag = "1"
	}
	tokenData := map[string]string{
		"apiKey":      credentials.APIKey,
		"apiSecret":   credentials.APISecret,
		"passphrase":  *credentials.Passphrase,
		"isSimulated": simFlag,
	}
	tokenBytes, err := json.Marshal(tokenData)
	if err != nil {
		return nil, fmt.Errorf("failed to create composite token: %w", err)
	}

	compositeToken := base64.StdEncoding.EncodeToString(tokenBytes)
	expiresAt := time.Now().Add(365 * 24 * time.Hour)
	return &client.AuthResponse{
		AccessToken: compositeToken,
		ExpiresAt:   expiresAt,
	}, nil
}

// RefreshToken - OKX doesn't need token refresh, API keys are permanent
func (c *OKXClient) RefreshToken(ctx context.Context, refreshToken string) (*client.AuthResponse, error) {
	return nil, fmt.Errorf("OKX uses API keys and doesn't support token refresh")
}

// GetPortfolio retrieves portfolio information from OKX
func (c *OKXClient) GetPortfolio(ctx context.Context, accessToken string) (*client.Portfolio, error) {
	apiKey, apiSecret, passphrase, isSimulated, err := c.parseCompositeToken(accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.makeRequest(ctx, "GET", "/api/v5/account/balance", nil, apiKey, apiSecret, passphrase, isSimulated)
	if err != nil {
		return nil, fmt.Errorf("failed to get portfolio: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("portfolio request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var balanceResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			TotalEq string `json:"totalEq"`
			Details []struct {
				Ccy       string `json:"ccy"`
				EqUsd     string `json:"eqUsd"`
				CashBal   string `json:"cashBal"`
				FrozenBal string `json:"frozenBal"`
			} `json:"details"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&balanceResp); err != nil {
		return nil, fmt.Errorf("failed to decode balance response: %w", err)
	}

	if balanceResp.Code != "0" {
		return nil, fmt.Errorf("OKX API error: %s", balanceResp.Msg)
	}

	if len(balanceResp.Data) == 0 {
		return &client.Portfolio{Currency: "USD", LastUpdated: time.Now()}, nil
	}

	data := balanceResp.Data[0]
	totalValue, _ := decimal.NewFromString(data.TotalEq)

	assetAllocation := make(map[string]decimal.Decimal)
	var cashBalance decimal.Decimal
	for _, detail := range data.Details {
		value, _ := decimal.NewFromString(detail.EqUsd)
		if !value.IsPositive() {
			continue
		}
		assetAllocation[detail.Ccy] = value
		if detail.Ccy == "USDT" || detail.Ccy == "USD" || detail.Ccy == "USDC" {
			cashBalance = cashBalance.Add(value)
		}
	}

	return &client.Portfolio{
		TotalValue:      totalValue,
		CashBalance:     cashBalance,
		Currency:        "USD",
		AssetAllocation: assetAllocation,
		LastUpdated:     time.Now(),
	}, nil
}

// GetPositions retrieves current positions from OKX
func (c *OKXClient) GetPositions(ctx context.Context, accessToken string) ([]client.Position, error) {
	apiKey, apiSecret, passphrase, isSimulated, err := c.parseCompositeToken(accessToken)
	if err != nil {
		return nil, err
	}
	resp, err := c.makeRequest(ctx, "GET", "/api/v5/account/positions", nil, apiKey, apiSecret, passphrase, isSimulated)
	if err != nil {
		return nil, fmt.Errorf("failed to get positions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("positions request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var positionsResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstId      string `json:"instId"`
			Pos         string `json:"pos"`
			AvgPx       string `json:"avgPx"`
			MarkPx      string `json:"markPx"`
			Upl         string `json:"upl"`
			UplRatio    string `json:"uplRatio"`
			NotionalUsd string `json:"notionalUsd"`
			Lever       string `json:"lever"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&positionsResp); err != nil {
		return nil, fmt.Errorf("failed to decode positions response: %w", err)
	}

	if positionsResp.Code != "0" {
		return nil, fmt.Errorf("OKX API error: %s", positionsResp.Msg)
	}

	positions := make([]client.Position, 0, len(positionsResp.Data))
	for _, pos := range positionsResp.Data {
		quantity, _ := decimal.NewFromString(pos.Pos)
		if quantity.IsZero() {
			continue
		}
		symbol := strings.Split(pos.InstId, "-")[0]
		uplRatio, _ := decimal.NewFromString(pos.UplRatio)
		unrealizedPLPct := uplRatio.Mul(decimal.NewFromInt(100)).InexactFloat64()
		avgPx, _ := decimal.NewFromString(pos.AvgPx)
		markPx, _ := decimal.NewFromString(pos.MarkPx)
		notional, _ := decimal.NewFromString(pos.NotionalUsd)
		upl, _ := decimal.NewFromString(pos.Upl)
		positions = append(positions, client.Position{
			Symbol:             symbol,
			Name:               pos.InstId,
			AssetType:          "crypto",
			Quantity:           quantity,
			AverageCostPerUnit: avgPx,
			CurrentPrice:       markPx,
			CurrentValue:       notional,
			UnrealizedGain:     upl,
			UnrealizedGainPct:  unrealizedPLPct,
			Currency:           "USD",
			Exchange:           "OKX",
			ExternalID:         pos.InstId,
			LastUpdated:        time.Now(),
		})
	}

	return positions, nil
}

// GetTransactions retrieves transaction history from OKX
func (c *OKXClient) GetTransactions(ctx context.Context, accessToken string, startDate, endDate time.Time) ([]client.Transaction, error) {
	return []client.Transaction{}, nil
}

// parseCompositeToken decodes the base64 JSON token.
func (c *OKXClient) parseCompositeToken(token string) (apiKey, apiSecret, passphrase string, isSimulated bool, err error) {
	tokenBytes, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return "", "", "", false, fmt.Errorf("invalid token format")
	}

	var data map[string]string
	if err := json.Unmarshal(tokenBytes, &data); err != nil {
		return "", "", "", false, fmt.Errorf("invalid token data")
	}

	return data["apiKey"], data["apiSecret"], data["passphrase"], data["isSimulated"] == "1", nil
}

// GetMarketPrice retrieves current market price for a symbol
func (c *OKXClient) GetMarketPrice(ctx context.Context, symbol string) (*client.MarketPrice, error) {
	instId := symbol + "-USDT"
	url := fmt.Sprintf("%s/api/v5/market/ticker?instId=%s", c.baseURL, instId)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create market price request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get market price: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("market price request failed with status %d", resp.StatusCode)
	}

	var priceResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			InstId  string `json:"instId"`
			Last    string `json:"last"`
			Open24h string `json:"open24h"`
			High24h string `json:"high24h"`
			Low24h  string `json:"low24h"`
			Vol24h  string `json:"vol24h"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&priceResp); err != nil {
		return nil, fmt.Errorf("failed to decode market price response: %w", err)
	}

	if priceResp.Code != "0" || len(priceResp.Data) == 0 {
		return nil, fmt.Errorf("OKX API error: %s", priceResp.Msg)
	}

	data := priceResp.Data[0]
	price, _ := decimal.NewFromString(data.Last)
	open, _ := decimal.NewFromString(data.Open24h)
	change := price.Sub(open)
	changePct := 0.0
	if open.IsPositive() {
		changePct = change.Div(open).Mul(decimal.NewFromInt(100)).InexactFloat64()
	}
	vol, _ := decimal.NewFromString(data.Vol24h)

	return &client.MarketPrice{
		Symbol:      symbol,
		Price:       price,
		Change:      change,
		ChangePct:   changePct,
		Volume:      vol.InexactFloat64(),
		Currency:    "USD",
		LastUpdated: time.Now(),
	}, nil
}

// GetBatchMarketPrices retrieves prices for multiple symbols
func (c *OKXClient) GetBatchMarketPrices(ctx context.Context, symbols []string) (map[string]*client.MarketPrice, error) {
	prices := make(map[string]*client.MarketPrice)
	for _, symbol := range symbols {
		price, err := c.GetMarketPrice(ctx, symbol)
		if err != nil {
			continue
		}
		prices[symbol] = price
	}
	return prices, nil
}
