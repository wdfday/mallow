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
		var unrealizedPL decimal.Decimal
		if p.UnrealizedPL != nil {
			unrealizedPL = *p.UnrealizedPL
		}
		positions[i] = exchange.ExchangePosition{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgEntryPrice,
			CurPrice: curPrice,
			// Side is already "long"/"short" from Alpaca's API, matching
			// exchange.PositionSide's convention directly.
			Side:          exchange.PositionSide(p.Side),
			UnrealizedPnL: unrealizedPL,
			// No per-position leverage/liquidation-price/margin-mode field
			// exists on Alpaca's Position — equities only, no perp-style margin.
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
			OrderID:       o.ID,
			ClientOrderID: o.ClientOrderID,
			Symbol:        o.Symbol,
			Side:          string(o.Side),
			Qty:           o.FilledQty,
			AvgPrice:      avg,
			FilledAt:      *o.FilledAt,
		})
	}

	return &exchange.AccountSnapshot{
		Cash:         acct.Cash,
		Equity:       acct.Equity,
		Positions:    positions,
		Transactions: txns,
		// Alpaca has one equity concept (cash + long/short market value);
		// AccountEquity mirrors Equity so Portfolio's prefer-exchange-equity
		// logic (see ApplySync) applies uniformly here too.
		AccountEquity: acct.Equity,
		Permissions: &exchange.AccountPermissions{
			CanTrade:        !acct.TradingBlocked && !acct.AccountBlocked,
			CanWithdraw:     !acct.TransfersBlocked && !acct.AccountBlocked,
			CanDeposit:      !acct.AccountBlocked,
			TradingBlocked:  acct.TradingBlocked,
			AccountBlocked:  acct.AccountBlocked,
			ShortingEnabled: acct.ShortingEnabled,
		},
	}, nil
}
