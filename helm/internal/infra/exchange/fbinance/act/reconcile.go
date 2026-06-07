package act

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"mallow/helm/internal/infra/exchange"
)

// ListOpenOrders returns all outstanding USDM futures orders.
func (c *Client) ListOpenOrders(ctx context.Context, creds exchange.Credentials, symbol string) ([]exchange.OrderResult, error) {
	futSvc := c.newFut(creds).NewListOpenOrdersService()
	if symbol != "" {
		futSvc = futSvc.Symbol(symbol)
	}
	futOrders, err := futSvc.Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("fbinance list open orders: %w", err)
	}

	results := make([]exchange.OrderResult, 0, len(futOrders))
	for _, o := range futOrders {
		results = append(results, exchange.OrderResult{
			ID:            strconv.FormatInt(o.OrderID, 10),
			ClientOrderID: o.ClientOrderID,
			Symbol:        o.Symbol,
			Side:          exchange.OrderSide(strings.ToLower(string(o.Side))),
			Status:        strings.ToLower(string(o.Status)),
			Qty:           parseDecimal(o.OrigQuantity),
			FilledQty:     parseDecimal(o.ExecutedQuantity),
		})
	}
	return results, nil
}

// ListPositions returns all currently held USDM futures positions (non-zero only).
func (c *Client) ListPositions(ctx context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	risk, err := c.newFut(creds).NewGetPositionRiskService().Do(ctx)
	if err != nil {
		slog.Warn("fbinance list positions: skipping", "err", err)
		return nil, fmt.Errorf("fbinance list positions: %w", err)
	}

	var results []exchange.PositionResult
	for _, p := range risk {
		amt := parseDecimal(p.PositionAmt)
		if amt.IsZero() {
			continue
		}
		side := exchange.Buy
		if amt.IsNegative() {
			side = exchange.Sell
			amt = amt.Neg()
		}
		results = append(results, exchange.PositionResult{
			Symbol:    p.Symbol,
			Side:      side,
			Qty:       amt,
			AvgPrice:  parseDecimal(p.EntryPrice),
			UnrealPnL: parseDecimal(p.UnRealizedProfit),
		})
	}
	return results, nil
}
