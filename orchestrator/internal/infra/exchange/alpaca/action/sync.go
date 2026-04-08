package action

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

// SyncAccount implements exchange.AccountSyncer.
// Fetches current cash, positions, and recent filled orders from Alpaca REST API.
// If since is non-nil, only orders filled after that time are returned.
func (c *Client) SyncAccount(_ context.Context, since *time.Time) (*exchange.AccountSnapshot, error) {
	acct, err := c.sdk.GetAccount()
	if err != nil {
		return nil, fmt.Errorf("alpaca sync: get account: %w", err)
	}

	sdkPositions, err := c.sdk.GetPositions()
	if err != nil {
		return nil, fmt.Errorf("alpaca sync: get positions: %w", err)
	}

	positions := make([]exchange.ExchangePosition, len(sdkPositions))
	for i, p := range sdkPositions {
		var curPrice decimal.Decimal
		if p.CurrentPrice != nil {
			curPrice = *p.CurrentPrice
		}
		positions[i] = exchange.ExchangePosition{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgEntryPrice,
			CurPrice: curPrice,
		}
	}

	// Fetch filled orders. When since is provided, restrict to orders after that time
	// so restarts only replay the gap, not the full history.
	ordersReq := alpacasdk.GetOrdersRequest{
		Status: "filled",
		Limit:  500,
	}
	if since != nil {
		ordersReq.After = *since
	}
	filledOrders, err := c.sdk.GetOrders(ordersReq)
	if err != nil {
		// Non-critical: log and continue without transactions.
		slog.Warn("alpaca sync: failed to fetch filled orders", "err", err)
		filledOrders = nil
	}

	txns := make([]exchange.AccountTransaction, 0, len(filledOrders))
	for _, o := range filledOrders {
		if o.FilledAt == nil {
			continue
		}
		var avg decimal.Decimal
		if o.FilledAvgPrice != nil {
			avg = *o.FilledAvgPrice
		}
		txns = append(txns, exchange.AccountTransaction{
			OrderID:  o.ID,
			Symbol:   o.Symbol,
			Side:     string(o.Side),
			Qty:      o.FilledQty,
			AvgPrice: avg,
			FilledAt: *o.FilledAt,
		})
	}

	return &exchange.AccountSnapshot{
		Cash:         acct.Cash,
		Equity:       acct.Equity,
		Positions:    positions,
		Transactions: txns,
	}, nil
}
