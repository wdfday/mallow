package act

import (
	"context"
	"log/slog"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// SubscribeDepth subscribes to BTCUSDT@depth10@100ms (or any symbol) and pushes
// each snapshot into ring. Reconnects automatically on disconnection.
// Returns immediately; the goroutine runs until ctx is cancelled.
func (c *Client) SubscribeDepth(ctx context.Context, symbol string, ring *exchange.DepthRing) {
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			err := c.subscribeDepthOnce(ctx, symbol, ring)
			if ctx.Err() != nil {
				return
			}
			slog.Warn("binance: depth stream disconnected, reconnecting in 2s",
				"symbol", symbol, "err", err)
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("binance: depth stream started", "symbol", symbol)
}

func (c *Client) subscribeDepthOnce(ctx context.Context, symbol string, ring *exchange.DepthRing) error {
	wsMu.Lock()
	gobinance.UseDemo = c.paper
	doneC, stopC, err := gobinance.WsPartialDepthServe100Ms(symbol, "5",
		func(e *gobinance.WsPartialDepthEvent) {
			var snap exchange.L2Snapshot
			snap.Symbol = symbol
			snap.Timestamp = time.Now().UTC()
			for i := 0; i < 5 && i < len(e.Bids); i++ {
				snap.Bids[i] = exchange.L2Level{
					Price: decimal.RequireFromString(e.Bids[i].Price),
					Size:  decimal.RequireFromString(e.Bids[i].Quantity),
				}
			}
			for i := 0; i < 5 && i < len(e.Asks); i++ {
				snap.Asks[i] = exchange.L2Level{
					Price: decimal.RequireFromString(e.Asks[i].Price),
					Size:  decimal.RequireFromString(e.Asks[i].Quantity),
				}
			}
			ring.Push(snap)
		},
		func(err error) {
			slog.Warn("binance: depth ws error", "symbol", symbol, "err", err)
		},
	)
	gobinance.UseDemo = false
	wsMu.Unlock()

	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		close(stopC)
		return nil
	case <-doneC:
		return nil
	}
}
