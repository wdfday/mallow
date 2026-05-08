package action

import (
	"context"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// GetCurrentPrice implements exchange.PriceFetcher via OKX REST ticker (public endpoint).
func (c *Client) GetCurrentPrice(ctx context.Context, _ exchange.Credentials, symbol string) (decimal.Decimal, error) {
	price, err := c.tickerLast(ctx, symbol)
	if err != nil {
		return decimal.Zero, fmt.Errorf("okx get price %s: %w", symbol, err)
	}
	return price, nil
}

// tickerLast fetches the last traded price for a symbol (public endpoint).
func (c *Client) tickerLast(ctx context.Context, instID string) (decimal.Decimal, error) {
	var resp struct {
		okxResponse
		Data []struct {
			Last string `json:"last"`
		} `json:"data"`
	}
	path := "/api/v5/market/ticker?instId=" + instID
	if err := c.doRequest(ctx, exchange.Credentials{}, http.MethodGet, path, nil, &resp); err != nil {
		return decimal.Zero, err
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return decimal.Zero, fmt.Errorf("ticker: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return parseDecimal(resp.Data[0].Last), nil
}
