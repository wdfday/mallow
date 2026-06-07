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
	"time"

	"github.com/shopspring/decimal"
	"mallow/helm/internal/infra/exchange/client"
)

const OKXBaseURL = "https://www.okx.com"

// OKXClient implements the BrokerClient interface for OKX Exchange.
type OKXClient struct {
	httpClient *http.Client
	baseURL    string
}

func NewOKXClient() *OKXClient {
	return &OKXClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    OKXBaseURL,
	}
}

// sign creates HMAC SHA256 signature for OKX API.
func (c *OKXClient) sign(timestamp, method, requestPath, body, secretKey string) string {
	h := hmac.New(sha256.New, []byte(secretKey))
	h.Write([]byte(timestamp + method + requestPath + body))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// doRequest makes an authenticated request to OKX API.
func (c *OKXClient) doRequest(ctx context.Context, method, path string, body interface{}, creds client.Credentials) (*http.Response, error) {
	if creds.Passphrase == nil || *creds.Passphrase == "" {
		return nil, fmt.Errorf("passphrase is required for OKX")
	}

	var bodyBytes []byte
	bodyStr := ""
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyStr = string(bodyBytes)
	}

	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	signature := c.sign(timestamp, method, path, bodyStr, creds.APISecret)

	url := c.baseURL + path
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, url, bytes.NewBuffer(bodyBytes))
	} else {
		req, err = http.NewRequestWithContext(ctx, method, url, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OK-ACCESS-KEY", creds.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", signature)
	req.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	req.Header.Set("OK-ACCESS-PASSPHRASE", *creds.Passphrase)
	if creds.IsPaper {
		req.Header.Set("x-simulated-trading", "1")
	}

	return c.httpClient.Do(req)
}

func (c *OKXClient) Validate(ctx context.Context, creds client.Credentials) error {
	resp, err := c.doRequest(ctx, "GET", "/api/v5/account/balance", nil, creds)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("authentication failed: %s", string(body))
	}
	return nil
}

func (c *OKXClient) GetPortfolio(ctx context.Context, creds client.Credentials) (*client.Portfolio, error) {
	resp, err := c.doRequest(ctx, "GET", "/api/v5/account/balance", nil, creds)
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
				Ccy     string `json:"ccy"`
				EqUsd   string `json:"eqUsd"`
				CashBal string `json:"cashBal"`
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

func (c *OKXClient) GetPositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	var positions []client.Position

	spotPositions, err := c.getSpotPositions(ctx, creds)
	if err != nil {
		return nil, err
	}
	positions = append(positions, spotPositions...)

	derivPositions, err := c.getDerivativePositions(ctx, creds)
	if err != nil {
		return nil, err
	}
	positions = append(positions, derivPositions...)

	return positions, nil
}

func (c *OKXClient) getSpotPositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	resp, err := c.doRequest(ctx, "GET", "/api/v5/account/balance", nil, creds)
	if err != nil {
		return nil, fmt.Errorf("okx balance for positions: %w", err)
	}
	defer resp.Body.Close()

	var balResp struct {
		Code string `json:"code"`
		Msg  string `json:"msg"`
		Data []struct {
			Details []struct {
				Ccy      string `json:"ccy"`
				AvailBal string `json:"availBal"`
				Eq       string `json:"eq"`
				EqUsd    string `json:"eqUsd"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&balResp); err != nil {
		return nil, fmt.Errorf("okx balance decode: %w", err)
	}
	if balResp.Code != "0" || len(balResp.Data) == 0 {
		return nil, nil
	}

	stablecoins := map[string]bool{"USDT": true, "USDC": true, "USD": true, "DAI": true, "BUSD": true}
	var positions []client.Position
	for _, d := range balResp.Data[0].Details {
		if stablecoins[d.Ccy] {
			continue
		}
		qty, _ := decimal.NewFromString(d.Eq)
		if !qty.IsPositive() {
			continue
		}
		valueUSD, _ := decimal.NewFromString(d.EqUsd)
		var curPrice decimal.Decimal
		if qty.IsPositive() {
			curPrice = valueUSD.Div(qty)
		}
		instID := d.Ccy + "-USDT"
		positions = append(positions, client.Position{
			Symbol:       instID,
			Name:         instID,
			AssetType:    "crypto",
			Quantity:     qty,
			CurrentPrice: curPrice,
			CurrentValue: valueUSD,
			Currency:     "USD",
			Exchange:     "OKX",
			ExternalID:   instID,
			LastUpdated:  time.Now(),
		})
	}
	return positions, nil
}

func (c *OKXClient) getDerivativePositions(ctx context.Context, creds client.Credentials) ([]client.Position, error) {
	resp, err := c.doRequest(ctx, "GET", "/api/v5/account/positions", nil, creds)
	if err != nil {
		return nil, fmt.Errorf("failed to get derivative positions: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("positions request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var posResp struct {
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
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&posResp); err != nil {
		return nil, fmt.Errorf("failed to decode positions response: %w", err)
	}
	if posResp.Code != "0" {
		return nil, fmt.Errorf("OKX API error: %s", posResp.Msg)
	}

	positions := make([]client.Position, 0, len(posResp.Data))
	for _, pos := range posResp.Data {
		quantity, _ := decimal.NewFromString(pos.Pos)
		if quantity.IsZero() {
			continue
		}
		uplRatio, _ := decimal.NewFromString(pos.UplRatio)
		avgPx, _ := decimal.NewFromString(pos.AvgPx)
		markPx, _ := decimal.NewFromString(pos.MarkPx)
		notional, _ := decimal.NewFromString(pos.NotionalUsd)
		upl, _ := decimal.NewFromString(pos.Upl)
		positions = append(positions, client.Position{
			Symbol:             pos.InstId,
			Name:               pos.InstId,
			AssetType:          "crypto",
			Quantity:           quantity,
			AverageCostPerUnit: avgPx,
			CurrentPrice:       markPx,
			CurrentValue:       notional,
			UnrealizedGain:     upl,
			UnrealizedGainPct:  uplRatio.Mul(decimal.NewFromInt(100)).InexactFloat64(),
			Currency:           "USD",
			Exchange:           "OKX",
			ExternalID:         pos.InstId,
			LastUpdated:        time.Now(),
		})
	}
	return positions, nil
}
