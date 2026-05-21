package act

import (
	"context"
	"log/slog"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// StreamOrders implements exchange.AccountStreamer.
// balanceHandler is ignored — Alpaca does not push balance updates on the trade-updates stream.
func (c *Client) StreamOrders(ctx context.Context, creds exchange.Credentials, handler func(exchange.OrderEvent), _ func(exchange.BalanceEvent)) error {
	slog.Info("alpaca: starting order stream")
	go c.streamOrdersLoop(ctx, creds, handler)
	return nil
}

func (c *Client) streamOrdersLoop(ctx context.Context, creds exchange.Credentials, handler func(exchange.OrderEvent)) {
	bo := exchange.Backoff{Min: 2 * time.Second, Max: 60 * time.Second, Factor: 2.0, Jitter: true}
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := c.newSDK(creds).StreamTradeUpdates(ctx, func(tu alpacasdk.TradeUpdate) {
			handleTradeUpdate(tu, handler)
		}, alpacasdk.StreamTradeUpdatesRequest{})
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			attempt = 0
		}
		wait := bo.Next(attempt)
		attempt++
		slog.Warn("alpaca: order stream disconnected, reconnecting", "err", err, "attempt", attempt, "retry_in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func handleTradeUpdate(tu alpacasdk.TradeUpdate, handler func(exchange.OrderEvent)) {
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
}
