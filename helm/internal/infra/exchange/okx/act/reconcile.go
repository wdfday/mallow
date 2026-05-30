package act

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// ListOpenOrders returns all pending orders across all instrument types.
// Delegates to GetPendingOrders which calls /api/v5/trade/orders-pending.
// When symbol is non-empty, only orders for that instId are returned.
func (c *Client) ListOpenOrders(ctx context.Context, creds exchange.Credentials, symbol string) ([]exchange.OrderResult, error) {
	results, err := c.GetPendingOrders(ctx, creds, symbol)
	if err != nil {
		return nil, fmt.Errorf("okx list open orders: %w", err)
	}
	return results, nil
}

// ListPositions returns all currently held positions across the unified account.
//
// OKX uses a Unified Account — spot holdings and derivatives positions live under
// the same /api/v5/account/balance and /api/v5/account/positions endpoints.
//
// Spot: non-stablecoin balances with equity > 0 are returned as long positions.
// Derivatives (SWAP/FUTURES): from /api/v5/account/positions, all instTypes.
func (c *Client) ListPositions(ctx context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	info, err := c.GetBalance(ctx, creds)
	if err != nil {
		return nil, fmt.Errorf("okx list positions (balance): %w", err)
	}

	var results []exchange.PositionResult

	// Spot holdings: non-stablecoin assets with positive equity
	for _, b := range info.Balances {
		if stablecoins[b.Currency] {
			continue
		}
		qty := decimal.NewFromFloat(b.Equity)
		if !qty.IsPositive() {
			continue
		}
		results = append(results, exchange.PositionResult{
			Symbol: b.Currency + "-USDT",
			Side:   exchange.Buy,
			Qty:    qty,
		})
	}

	// Derivatives: SWAP + FUTURES positions
	derivs, err := c.GetPositions(ctx, creds, "")
	if err != nil {
		return nil, fmt.Errorf("okx list positions (derivatives): %w", err)
	}
	for _, p := range derivs {
		qty := decimal.NewFromFloat(p.Pos)
		if qty.IsZero() {
			continue
		}
		side := exchange.Buy
		switch p.PosSide {
		case "short":
			side = exchange.Sell
		case "net":
			if qty.IsNegative() {
				side = exchange.Sell
				qty = qty.Neg()
			}
		}
		results = append(results, exchange.PositionResult{
			Symbol:    p.InstID,
			Side:      side,
			Qty:       qty,
			AvgPrice:  decimal.NewFromFloat(p.AvgPx),
			UnrealPnL: decimal.NewFromFloat(p.Upl),
		})
	}

	return results, nil
}
