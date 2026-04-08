package action

import (
	"context"
	"log/slog"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

// TradeUpdateHandler is a callback invoked on each order lifecycle event
// (fill, partial_fill, canceled, expired, etc).
type TradeUpdateHandler func(update alpacasdk.TradeUpdate)

// StreamTradeUpdates subscribes to real-time order lifecycle events.
// Blocks until ctx is cancelled or an unrecoverable error occurs.
// Events: new, fill, partial_fill, canceled, expired, replaced, rejected, etc.
func (c *Client) StreamTradeUpdates(ctx context.Context, handler TradeUpdateHandler) error {
	slog.Info("alpaca: starting trade updates stream")
	return c.sdk.StreamTradeUpdates(ctx, func(tu alpacasdk.TradeUpdate) {
		handler(tu)
	}, alpacasdk.StreamTradeUpdatesRequest{})
}

// StreamTradeUpdatesInBackground starts listening for trade updates in a
// background goroutine. It automatically reconnects on failure.
func (c *Client) StreamTradeUpdatesInBackground(ctx context.Context, handler TradeUpdateHandler) {
	slog.Info("alpaca: starting trade updates stream (background)")
	c.sdk.StreamTradeUpdatesInBackground(ctx, func(tu alpacasdk.TradeUpdate) {
		handler(tu)
	})
}

// StreamFills implements exchange.AccountStreamer. Starts a background WebSocket
// listener for fill events; reconnects automatically on failure.
// Only "fill" and "partial_fill" events are forwarded to the handler.
func (c *Client) StreamFills(ctx context.Context, handler func(exchange.FillEvent)) error {
	slog.Info("alpaca: starting fill stream")
	c.sdk.StreamTradeUpdatesInBackground(ctx, func(tu alpacasdk.TradeUpdate) {
		if tu.Event != "fill" && tu.Event != "partial_fill" {
			return
		}
		side := exchange.Buy
		if string(tu.Order.Side) == "sell" {
			side = exchange.Sell
		}
		var avg decimal.Decimal
		if tu.Order.FilledAvgPrice != nil {
			avg = *tu.Order.FilledAvgPrice
		}
		handler(exchange.FillEvent{
			OrderID:   tu.Order.ID,
			Symbol:    tu.Order.Symbol,
			Side:      side,
			FilledQty: tu.Order.FilledQty,
			FilledAvg: avg,
			Timestamp: time.Now().UTC(),
		})
	})
	return nil
}
