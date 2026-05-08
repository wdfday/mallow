package action

import (
	"context"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// SupportsFutures implements exchange.FuturesTrader — Bybit supports linear perpetuals.
func (c *Client) SupportsFutures() bool { return true }

// SetLeverage sets buy/sell leverage for a linear perpetual symbol.
// marginType: "cross" | "isolated"
func (c *Client) SetLeverage(ctx context.Context, creds exchange.Credentials, symbol string, leverage int, marginType string) error {
	lev := fmt.Sprintf("%d", leverage)
	body := map[string]any{
		"category":     "linear",
		"symbol":       symbol,
		"buyLeverage":  lev,
		"sellLeverage": lev,
	}

	var resp apiResponse[struct{}]
	if err := c.doSigned(ctx, creds, "POST", "/v5/position/set-leverage", body, &resp); err != nil {
		return fmt.Errorf("bybit set leverage %s x%d: %w", symbol, leverage, err)
	}
	// retCode 110043 = leverage not modified (already set) — non-fatal
	if resp.RetCode != 0 && resp.RetCode != 110043 {
		return fmt.Errorf("bybit set leverage: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}

	// Set margin mode: cross=0, isolated=1
	tradeMode := 0
	if strings.ToLower(marginType) == "isolated" {
		tradeMode = 1
	}
	modeBody := map[string]any{
		"category":     "linear",
		"symbol":       symbol,
		"tradeMode":    tradeMode,
		"buyLeverage":  lev,
		"sellLeverage": lev,
	}
	var modeResp apiResponse[struct{}]
	if err := c.doSigned(ctx, creds, "POST", "/v5/position/switch-isolated", modeBody, &modeResp); err != nil {
		return fmt.Errorf("bybit set margin mode %s: %w", symbol, err)
	}
	// 110026 = margin mode already set — non-fatal
	if modeResp.RetCode != 0 && modeResp.RetCode != 110026 {
		return fmt.Errorf("bybit set margin mode: code=%d msg=%s", modeResp.RetCode, modeResp.RetMsg)
	}
	return nil
}

// FundingRate returns the latest funding rate for a linear perpetual symbol.
func (c *Client) FundingRate(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	body := map[string]string{
		"category": "linear",
		"symbol":   symbol,
		"limit":    "1",
	}

	var resp apiResponse[fundingHistoryResult]
	if err := c.doSigned(ctx, creds, "GET", "/v5/market/funding/history", body, &resp); err != nil {
		return decimal.Zero, fmt.Errorf("bybit funding rate %s: %w", symbol, err)
	}
	if resp.RetCode != 0 || len(resp.Result.List) == 0 {
		return decimal.Zero, fmt.Errorf("bybit funding rate: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	return parseDecimal(resp.Result.List[0].FundingRate), nil
}

// MarkPrice returns the current mark price for a linear perpetual symbol.
func (c *Client) MarkPrice(ctx context.Context, creds exchange.Credentials, symbol string) (decimal.Decimal, error) {
	body := map[string]string{"category": "linear", "symbol": symbol}

	var resp apiResponse[tickerResult]
	if err := c.doSigned(ctx, creds, "GET", "/v5/market/tickers", body, &resp); err != nil {
		return decimal.Zero, fmt.Errorf("bybit mark price %s: %w", symbol, err)
	}
	if resp.RetCode != 0 || len(resp.Result.List) == 0 {
		return decimal.Zero, fmt.Errorf("bybit mark price: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	return parseDecimal(resp.Result.List[0].MarkPrice), nil
}

// --- types ---

type fundingHistoryResult struct {
	List []struct {
		Symbol      string `json:"symbol"`
		FundingRate string `json:"fundingRate"`
	} `json:"list"`
}

type tickerResult struct {
	List []struct {
		Symbol    string `json:"symbol"`
		MarkPrice string `json:"markPrice"`
		LastPrice string `json:"lastPrice"`
	} `json:"list"`
}
