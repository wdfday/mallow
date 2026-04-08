package ex

import (
	"context"
	"log/slog"
	"sync"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"
	"github.com/shopspring/decimal"
)

// wsMu guards the gobinance.UseTestnet global flag.
var wsMu sync.Mutex

// Subscribe connects to Binance WebSocket trade streams for the given symbols
// and fans out live trade prices to all registered PriceHandlers.
// Each symbol gets its own WsTradeServe connection; futures symbols (ending
// with "PERP" or in the futures subset) use the futures WsTradeServe.
// Reconnects automatically on disconnection. Blocks until ctx is cancelled.
func (c *Client) Subscribe(ctx context.Context, symbols []string) error {
	for _, sym := range symbols {
		go c.streamSymbol(ctx, sym)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (c *Client) streamSymbol(ctx context.Context, symbol string) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.streamSymbolOnce(ctx, symbol)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("binance/ex: trade stream disconnected, reconnecting in 3s",
			"symbol", symbol, "err", err)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) streamSymbolOnce(ctx context.Context, symbol string) error {
	handler := func(price decimal.Decimal) {
		c.dispatchPrice(symbol, price)
	}
	errHandler := func(err error) {
		slog.Warn("binance/ex: ws trade error", "symbol", symbol, "err", err)
	}

	wsMu.Lock()
	gobinance.UseDemo = c.testnet
	doneC, stopC, err := gobinance.WsTradeServe(symbol, func(event *gobinance.WsTradeEvent) {
		if p, err := decimal.NewFromString(event.Price); err == nil {
			handler(p)
		}
	}, errHandler)
	gobinance.UseDemo = false
	wsMu.Unlock()

	if err != nil {
		// Fall back to futures stream for symbols that are perpetual contracts.
		return c.streamFuturesSymbolOnce(ctx, symbol)
	}

	select {
	case <-ctx.Done():
		close(stopC)
		return nil
	case <-doneC:
		return context.Canceled // triggers reconnect
	}
}

// streamFuturesSymbolOnce uses the futures WsTradeServe for perpetual contracts.
func (c *Client) streamFuturesSymbolOnce(ctx context.Context, symbol string) error {
	errHandler := func(err error) {
		slog.Warn("binance/ex: futures ws trade error", "symbol", symbol, "err", err)
	}

	doneC, stopC, err := futures.WsAggTradeServe(symbol, func(event *futures.WsAggTradeEvent) {
		if p, err := decimal.NewFromString(event.Price); err == nil {
			c.dispatchPrice(event.Symbol, p)
		}
	}, errHandler)
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		close(stopC)
		return nil
	case <-doneC:
		return context.Canceled
	}
}

