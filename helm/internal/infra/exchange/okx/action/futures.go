package action

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// SupportsFutures implements exchange.FuturesTrader — OKX supports SWAP perpetuals.
func (c *Client) SupportsFutures() bool { return true }

// SetLeverage sets leverage and margin mode for a SWAP instrument.
// marginType: "cross" | "isolated"
func (c *Client) SetLeverage(ctx context.Context, creds exchange.Credentials, symbol string, leverage int, marginType string) error {
	mgnMode := "cross"
	if strings.ToLower(marginType) == "isolated" {
		mgnMode = "isolated"
	}

	body := map[string]any{
		"instId":  symbol,
		"lever":   fmt.Sprintf("%d", leverage),
		"mgnMode": mgnMode,
	}

	var resp struct {
		okxResponse
		Data []struct {
			Lever   string `json:"lever"`
			MgnMode string `json:"mgnMode"`
		} `json:"data"`
	}

	if err := c.doRequest(ctx, creds, http.MethodPost, "/api/v5/account/set-leverage", body, &resp); err != nil {
		return fmt.Errorf("okx set leverage %s x%d: %w", symbol, leverage, err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("okx set leverage: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// FundingRate returns the latest funding rate for a SWAP instrument (public endpoint).
func (c *Client) FundingRate(ctx context.Context, symbol string) (decimal.Decimal, error) {
	path := "/api/v5/public/funding-rate?instId=" + symbol

	var resp struct {
		okxResponse
		Data []struct {
			FundingRate string `json:"fundingRate"`
		} `json:"data"`
	}

	if err := c.doRequest(ctx, exchange.Credentials{}, http.MethodGet, path, nil, &resp); err != nil {
		return decimal.Zero, fmt.Errorf("okx funding rate %s: %w", symbol, err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return decimal.Zero, fmt.Errorf("okx funding rate: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return parseDecimal(resp.Data[0].FundingRate), nil
}

// MarkPrice returns the current mark price for a SWAP instrument (public endpoint).
func (c *Client) MarkPrice(ctx context.Context, symbol string) (decimal.Decimal, error) {
	path := "/api/v5/public/mark-price?instType=SWAP&instId=" + symbol

	var resp struct {
		okxResponse
		Data []struct {
			MarkPx string `json:"markPx"`
		} `json:"data"`
	}

	if err := c.doRequest(ctx, exchange.Credentials{}, http.MethodGet, path, nil, &resp); err != nil {
		return decimal.Zero, fmt.Errorf("okx mark price %s: %w", symbol, err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return decimal.Zero, fmt.Errorf("okx mark price: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return parseDecimal(resp.Data[0].MarkPx), nil
}
