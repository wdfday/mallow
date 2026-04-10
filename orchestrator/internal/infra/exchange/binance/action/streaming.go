package action

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"

	"orchestrator/internal/infra/exchange"
)

// wsMu guards gobinance.UseTestnet global flag.
var wsMu sync.Mutex

// StreamOrders implements exchange.AccountStreamer.
// For spot it uses WsUserDataServeSignature (signature-based, works on demo/testnet)
// or WsUserDataServe (listen-key, production fallback).
// Futures streaming uses the listen-key flow (production only).
func (c *Client) StreamOrders(ctx context.Context, handler func(exchange.OrderEvent)) error {
	go c.streamSpotOrders(ctx, handler)
	if !c.testnet {
		go c.streamFuturesOrders(ctx, handler)
	} else {
		slog.Info("binance: futures order streaming skipped on demo/testnet")
	}
	slog.Info("binance: order streaming started")
	return nil
}

// ── Spot ──────────────────────────────────────────────────────────────────────

func (c *Client) streamSpotOrders(ctx context.Context, handler func(exchange.OrderEvent)) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.streamSpotOrdersOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("binance: spot order stream disconnected, reconnecting in 5s", "err", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) streamSpotOrdersOnce(ctx context.Context, handler func(exchange.OrderEvent)) error {
	if c.testnet {
		return c.streamSpotOrdersSignature(ctx, handler)
	}
	return c.streamSpotOrdersListenKey(ctx, handler)
}

// streamSpotOrdersSignature uses WsUserDataServeSignature — no listen key needed.
// Works on demo (wss://demo-ws-api.binance.com/ws-api/v3) and production.
func (c *Client) streamSpotOrdersSignature(ctx context.Context, handler func(exchange.OrderEvent)) error {
	wsMu.Lock()
	gobinance.UseDemo = c.testnet
	doneC, stopC, err := gobinance.WsUserDataServeSignature(
		c.apiKey, c.apiSecret, "HMAC", 0,
		c.spotOrderHandler(handler),
		func(err error) { slog.Warn("binance: spot ws error", "err", err) },
	)
	gobinance.UseDemo = false
	wsMu.Unlock()
	if err != nil {
		return fmt.Errorf("ws spot user data (signature): %w", err)
	}
	return c.waitSpotStream(ctx, stopC, doneC, "", false)
}

// streamSpotOrdersListenKey is the classic listen-key flow (production only).
func (c *Client) streamSpotOrdersListenKey(ctx context.Context, handler func(exchange.OrderEvent)) error {
	listenKey, err := c.spot.NewStartUserStreamService().Do(ctx)
	if err != nil {
		return fmt.Errorf("start spot user stream: %w", err)
	}

	wsMu.Lock()
	doneC, stopC, err := gobinance.WsUserDataServe(listenKey, c.spotOrderHandler(handler), func(err error) {
		slog.Warn("binance: spot ws error", "err", err)
	})
	wsMu.Unlock()
	if err != nil {
		return fmt.Errorf("ws spot user data: %w", err)
	}
	return c.waitSpotStream(ctx, stopC, doneC, listenKey, true)
}

// spotOrderHandler returns a WsUserDataHandler that converts execution reports to OrderEvents.
func (c *Client) spotOrderHandler(handler func(exchange.OrderEvent)) gobinance.WsUserDataHandler {
	return func(event *gobinance.WsUserDataEvent) {
		if event.Event != gobinance.UserDataEventTypeExecutionReport {
			return
		}
		ou := event.OrderUpdate
		side := exchange.Buy
		if gobinance.SideType(ou.Side) == gobinance.SideTypeSell {
			side = exchange.Sell
		}
		ts := time.UnixMilli(ou.TransactionTime).UTC()
		orderID := strconv.FormatInt(ou.Id, 10)

		switch ou.ExecutionType {
		case "NEW":
			handler(exchange.OrderEvent{
				Type:      exchange.OrderEventLive,
				OrderID:   orderID,
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.Volume),
				Timestamp: ts,
			})
		case "TRADE":
			evType := exchange.OrderEventPartialFill
			if ou.Status == "FILLED" {
				evType = exchange.OrderEventFilled
			}
			qty := parseDecimal(ou.LatestVolume)
			if !qty.IsPositive() {
				return
			}
			handler(exchange.OrderEvent{
				Type:      evType,
				OrderID:   orderID,
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.Volume),
				FilledQty: qty,
				FilledAvg: parseDecimal(ou.LatestPrice),
				Timestamp: ts,
			})
		case "CANCELED", "EXPIRED", "REJECTED":
			handler(exchange.OrderEvent{
				Type:      exchange.OrderEventCanceled,
				OrderID:   orderID,
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.Volume),
				Timestamp: ts,
			})
		}
	}
}

// waitSpotStream blocks until ctx is done or the stream closes.
// If keepAlive=true it sends periodic keep-alives for the given listenKey.
func (c *Client) waitSpotStream(ctx context.Context, stopC, doneC chan struct{}, listenKey string, keepAlive bool) error {
	var ticker *time.Ticker
	var tickC <-chan time.Time
	if keepAlive && listenKey != "" {
		ticker = time.NewTicker(25 * time.Minute)
		tickC = ticker.C
		defer ticker.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			close(stopC)
			return nil
		case <-tickC:
			if err := c.spot.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(ctx); err != nil {
				slog.Warn("binance: spot listen key keep-alive failed", "err", err)
			}
		case <-doneC:
			return fmt.Errorf("spot user stream closed")
		}
	}
}

// ── Futures ───────────────────────────────────────────────────────────────────

func (c *Client) streamFuturesOrders(ctx context.Context, handler func(exchange.OrderEvent)) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.streamFuturesOrdersOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("binance: futures order stream disconnected, reconnecting in 5s", "err", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) streamFuturesOrdersOnce(ctx context.Context, handler func(exchange.OrderEvent)) error {
	listenKey, err := c.fut.NewStartUserStreamService().Do(ctx)
	if err != nil {
		return fmt.Errorf("start futures user stream: %w", err)
	}

	doneC, stopC, err := futures.WsUserDataServe(listenKey, func(event *futures.WsUserDataEvent) {
		if event.Event != futures.UserDataEventTypeOrderTradeUpdate {
			return
		}
		ou := event.OrderTradeUpdate
		side := exchange.Buy
		if ou.Side == futures.SideTypeSell {
			side = exchange.Sell
		}
		ts := time.UnixMilli(ou.TradeTime).UTC()
		orderID := strconv.FormatInt(ou.ID, 10)

		switch ou.ExecutionType {
		case "NEW":
			handler(exchange.OrderEvent{
				Type:      exchange.OrderEventLive,
				OrderID:   orderID,
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.OriginalQty),
				Timestamp: ts,
			})
		case "TRADE":
			evType := exchange.OrderEventPartialFill
			if ou.Status == futures.OrderStatusTypeFilled {
				evType = exchange.OrderEventFilled
			}
			qty := parseDecimal(ou.LastFilledQty)
			if !qty.IsPositive() {
				return
			}
			handler(exchange.OrderEvent{
				Type:      evType,
				OrderID:   orderID,
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.OriginalQty),
				FilledQty: qty,
				FilledAvg: parseDecimal(ou.LastFilledPrice),
				Timestamp: ts,
			})
		case "CANCELED", "EXPIRED", "CALCULATED":
			handler(exchange.OrderEvent{
				Type:      exchange.OrderEventCanceled,
				OrderID:   orderID,
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.OriginalQty),
				Timestamp: ts,
			})
		}
	}, func(err error) {
		slog.Warn("binance: futures ws error", "err", err)
	})
	if err != nil {
		return fmt.Errorf("ws futures user data: %w", err)
	}

	keepAlive := time.NewTicker(25 * time.Minute)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			close(stopC)
			return nil
		case <-keepAlive.C:
			if err := c.fut.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(ctx); err != nil {
				slog.Warn("binance: futures listen key keep-alive failed", "err", err)
			}
		case <-doneC:
			return fmt.Errorf("futures user stream closed")
		}
	}
}
