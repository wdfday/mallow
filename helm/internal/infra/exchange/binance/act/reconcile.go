package act

import (
	"context"
	"fmt"
	"strconv"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// ListOpenOrders returns all outstanding spot orders.
// When symbol is empty, all open orders are fetched (higher weight on Binance).
func (c *Client) ListOpenOrders(ctx context.Context, creds exchange.Credentials, symbol string) ([]exchange.OrderResult, error) {
	spotSvc := c.newSpot(creds).NewListOpenOrdersService()
	if symbol != "" {
		spotSvc = spotSvc.Symbol(symbol)
	}
	spotOrders, err := spotSvc.Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance list open orders (spot): %w", err)
	}

	results := make([]exchange.OrderResult, 0, len(spotOrders))
	for _, o := range spotOrders {
		filledQty := parseDecimal(o.ExecutedQuantity)
		quoteQty := parseDecimal(o.CummulativeQuoteQuantity)
		var filledAvg decimal.Decimal
		if filledQty.IsPositive() {
			filledAvg = quoteQty.Div(filledQty)
		}
		results = append(results, exchange.OrderResult{
			ID:            o.Symbol + ":" + strconv.FormatInt(o.OrderID, 10),
			ClientOrderID: o.ClientOrderID,
			Symbol:        o.Symbol,
			Side:          binanceSide(o.Side),
			Status:        string(o.Status),
			Qty:           parseDecimal(o.OrigQuantity),
			FilledQty:     filledQty,
			FilledAvg:     filledAvg,
		})
	}

	return results, nil
}

// ListPositions returns all currently held spot positions (non-stablecoin balances).
func (c *Client) ListPositions(ctx context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	acct, err := c.newSpot(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance list positions (spot): %w", err)
	}

	var results []exchange.PositionResult
	for _, b := range acct.Balances {
		free := parseDecimal(b.Free)
		locked := parseDecimal(b.Locked)
		total := free.Add(locked)
		if !total.IsPositive() {
			continue
		}
		switch b.Asset {
		case "USDT", "BUSD", "USDC", "FDUSD", "TUSD", "DAI":
			continue // stablecoins are cash, not positions
		}
		results = append(results, exchange.PositionResult{
			Symbol: b.Asset + "USDT",
			Side:   exchange.Buy,
			Qty:    total,
		})
	}

	return results, nil
}
