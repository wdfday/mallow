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

// StreamOrders implements exchange.AccountStreamer. Starts a background WebSocket
// listener for all order lifecycle events; reconnects automatically on failure.
func (c *Client) StreamOrders(ctx context.Context, handler func(exchange.OrderEvent)) error {
	slog.Info("alpaca: starting order stream")
	c.sdk.StreamTradeUpdatesInBackground(ctx, func(tu alpacasdk.TradeUpdate) {
		side := exchange.Buy
		if string(tu.Order.Side) == "sell" {
			side = exchange.Sell
		}
		ts := time.Now().UTC()
		var origQty decimal.Decimal
		if tu.Order.Qty != nil {
			origQty = *tu.Order.Qty
		}

		switch tu.Event {
		case "new", "pending_new", "accepted":
			handler(exchange.OrderEvent{
				Type:      exchange.OrderEventLive,
				OrderID:   tu.Order.ID,
				Symbol:    tu.Order.Symbol,
				Side:      side,
				Qty:       origQty,
				Timestamp: ts,
			})
		case "fill", "partial_fill":
			evType := exchange.OrderEventPartialFill
			if tu.Event == "fill" {
				evType = exchange.OrderEventFilled
			}
			var avg decimal.Decimal
			if tu.Order.FilledAvgPrice != nil {
				avg = *tu.Order.FilledAvgPrice
			}
			qty := tu.Order.FilledQty
			if !qty.IsPositive() {
				return
			}
			handler(exchange.OrderEvent{
				Type:      evType,
				OrderID:   tu.Order.ID,
				Symbol:    tu.Order.Symbol,
				Side:      side,
				Qty:       origQty,
				FilledQty: qty,
				FilledAvg: avg,
				Timestamp: ts,
			})
		case "canceled", "expired", "replaced", "rejected":
			handler(exchange.OrderEvent{
				Type:      exchange.OrderEventCanceled,
				OrderID:   tu.Order.ID,
				Symbol:    tu.Order.Symbol,
				Side:      side,
				Qty:       origQty,
				Timestamp: ts,
			})
		}
	})
	return nil
}
