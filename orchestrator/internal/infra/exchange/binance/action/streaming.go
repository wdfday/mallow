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

// StreamFills implements exchange.AccountStreamer.
// For spot bots (req.Market != futures) it uses the spot user data stream.
// For futures bots it uses the futures user data stream.
func (c *Client) StreamFills(ctx context.Context, handler func(exchange.FillEvent)) error {
	go c.streamSpotFills(ctx, handler)
	if c.testnet {
		// Testnet futures stream is optional; skip to avoid noise.
		slog.Info("binance: futures fill streaming skipped on testnet")
	} else {
		go c.streamFuturesFills(ctx, handler)
	}
	slog.Info("binance: fill streaming started")
	return nil
}

// ── Spot ──────────────────────────────────────────────────────────────────────

func (c *Client) streamSpotFills(ctx context.Context, handler func(exchange.FillEvent)) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.streamSpotFillsOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("binance: spot fill stream disconnected, reconnecting in 5s", "err", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) streamSpotFillsOnce(ctx context.Context, handler func(exchange.FillEvent)) error {
	listenKey, err := c.spot.NewStartUserStreamService().Do(ctx)
	if err != nil {
		return fmt.Errorf("start spot user stream: %w", err)
	}

	wsMu.Lock()
	gobinance.UseDemo = c.testnet
	doneC, stopC, err := gobinance.WsUserDataServe(listenKey, func(event *gobinance.WsUserDataEvent) {
		if event.Event != gobinance.UserDataEventTypeExecutionReport {
			return
		}
		ou := event.OrderUpdate
		if ou.ExecutionType != "TRADE" {
			return
		}
		side := exchange.Buy
		if gobinance.SideType(ou.Side) == gobinance.SideTypeSell {
			side = exchange.Sell
		}
		handler(exchange.FillEvent{
			OrderID:   strconv.FormatInt(ou.Id, 10),
			Symbol:    ou.Symbol,
			Side:      side,
			FilledQty: parseFloat(ou.LatestVolume),
			FilledAvg: parseFloat(ou.LatestPrice),
			Timestamp: time.UnixMilli(ou.TransactionTime).UTC(),
		})
	}, func(err error) {
		slog.Warn("binance: spot ws error", "err", err)
	})
	gobinance.UseDemo = false
	wsMu.Unlock()

	if err != nil {
		return fmt.Errorf("ws spot user data: %w", err)
	}

	keepAlive := time.NewTicker(25 * time.Minute)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			close(stopC)
			return nil
		case <-keepAlive.C:
			if err := c.spot.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(ctx); err != nil {
				slog.Warn("binance: spot listen key keep-alive failed", "err", err)
			}
		case <-doneC:
			return fmt.Errorf("spot user stream closed")
		}
	}
}

// ── Futures ───────────────────────────────────────────────────────────────────

func (c *Client) streamFuturesFills(ctx context.Context, handler func(exchange.FillEvent)) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.streamFuturesFillsOnce(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("binance: futures fill stream disconnected, reconnecting in 5s", "err", err)
		select {
		case <-time.After(5 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) streamFuturesFillsOnce(ctx context.Context, handler func(exchange.FillEvent)) error {
	listenKey, err := c.fut.NewStartUserStreamService().Do(ctx)
	if err != nil {
		return fmt.Errorf("start futures user stream: %w", err)
	}

	doneC, stopC, err := futures.WsUserDataServe(listenKey, func(event *futures.WsUserDataEvent) {
		if event.Event != futures.UserDataEventTypeOrderTradeUpdate {
			return
		}
		ou := event.OrderTradeUpdate
		if ou.ExecutionType != "TRADE" {
			return
		}
		side := exchange.Buy
		if ou.Side == futures.SideTypeSell {
			side = exchange.Sell
		}
		handler(exchange.FillEvent{
			OrderID:   strconv.FormatInt(ou.ID, 10),
			Symbol:    ou.Symbol,
			Side:      side,
			FilledQty: parseFloat(ou.LastFilledQty),
			FilledAvg: parseFloat(ou.LastFilledPrice),
			Timestamp: time.UnixMilli(ou.TradeTime).UTC(),
		})
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
