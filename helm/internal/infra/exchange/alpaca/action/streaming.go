package action

import (
	"context"
	"log/slog"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// TradeUpdateHandler is a callback invoked on each order lifecycle event.
type TradeUpdateHandler func(update alpacasdk.TradeUpdate)

// StreamTradeUpdates subscribes to real-time order lifecycle events (raw SDK events).
func (c *Client) StreamTradeUpdates(ctx context.Context, creds exchange.Credentials, handler TradeUpdateHandler) error {
	slog.Info("alpaca: starting trade updates stream")
	return c.newSDK(creds).StreamTradeUpdates(ctx, func(tu alpacasdk.TradeUpdate) {
		handler(tu)
	}, alpacasdk.StreamTradeUpdatesRequest{})
}

// StreamOrders implements exchange.AccountStreamer.
func (c *Client) StreamOrders(ctx context.Context, creds exchange.Credentials, handler func(exchange.OrderEvent)) error {
	slog.Info("alpaca: starting order stream")
	c.newSDK(creds).StreamTradeUpdatesInBackground(ctx, func(tu alpacasdk.TradeUpdate) {
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
				TradeID:   tu.Order.ID + "_open",
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
			tradeID := tu.ExecutionID
			if tradeID == "" {
				tradeID = tu.Order.ID + "_fill"
			}
			handler(exchange.OrderEvent{
				Type:      evType,
				OrderID:   tu.Order.ID,
				TradeID:   tradeID,
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
				TradeID:   tu.Order.ID + "_cancel",
				Symbol:    tu.Order.Symbol,
				Side:      side,
				Qty:       origQty,
				Timestamp: ts,
			})
		}
	})
	return nil
}
