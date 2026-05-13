package act

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// SyncAccount implements exchange.AccountSyncer.
func (c *Client) SyncAccount(_ context.Context, creds exchange.Credentials, since *time.Time) (*exchange.AccountSnapshot, error) {
	sdk := c.newSDK(creds)

	acct, err := sdk.GetAccount()
	if err != nil {
		return nil, fmt.Errorf("alpaca sync: get account: %w", err)
	}

	sdkPositions, err := sdk.GetPositions()
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

	ordersReq := alpacasdk.GetOrdersRequest{
		Status: "filled",
		Limit:  500,
	}
	if since != nil {
		ordersReq.After = *since
	}
	filledOrders, err := sdk.GetOrders(ordersReq)
	if err != nil {
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
