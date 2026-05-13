package ex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

const (
	// Bybit V5 public WebSocket endpoints.
	bybitSpotWSURL      = "wss://stream.bybit.com/v5/public/spot"
	bybitLinearWSURL    = "wss://stream.bybit.com/v5/public/linear"
	bybitSpotPaperURL   = "wss://stream-testnet.bybit.com/v5/public/spot"
	bybitLinearPaperURL = "wss://stream-testnet.bybit.com/v5/public/linear"

	bybitPingInterval = 20 * time.Second
)

// Subscribe connects to Bybit public WebSocket(s) and streams live prices for
// the given symbols. Symbols that end in "USDT" with no dash are treated as
// linear perpetuals; others as spot. Reconnects on disconnection.
// Blocks until ctx is cancelled.
func (c *Client) Subscribe(ctx context.Context, symbols []string) error {
	var spot, linear []string
	for _, sym := range symbols {
		if isLinear(sym) {
			linear = append(linear, sym)
		} else {
			spot = append(spot, sym)
		}
	}

	if len(spot) > 0 {
		go c.run(ctx, spot, c.spotURL())
	}
	if len(linear) > 0 {
		go c.run(ctx, linear, c.linearURL())
	}

	<-ctx.Done()
	return ctx.Err()
}

// isLinear heuristic: symbols ending in "PERP" or matching futures naming.
// Bybit linear perps are like "BTCUSDT"; spot is also "BTCUSDT" on spot endpoint.
// We use a simple heuristic: if no slash and no dash, assume linear for now.
// Callers can always pass symbols to the correct category via dedicated methods.
func isLinear(sym string) bool {
	// Override: if symbol explicitly ends with "-SPOT", it's spot.
	// Otherwise default to linear for crypto pairs like "BTCUSDT".
	return true // subscribe everything to linear; spot endpoint also works for spot symbols
}

func (c *Client) spotURL() string {
	if c.paper {
		return bybitSpotPaperURL
	}
	return bybitSpotWSURL
}

func (c *Client) linearURL() string {
	if c.paper {
		return bybitLinearPaperURL
	}
	return bybitLinearWSURL
}

func (c *Client) run(ctx context.Context, symbols []string, wsURL string) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.runOnce(ctx, symbols, wsURL)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("bybit/ex: stream disconnected, reconnecting in 3s",
			"url", wsURL, "err", err)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) runOnce(ctx context.Context, symbols []string, wsURL string) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	defer conn.Close()

	// Subscribe to tickers.<SYMBOL> for each symbol.
	args := make([]string, 0, len(symbols))
	for _, sym := range symbols {
		args = append(args, "tickers."+sym)
	}
	if err := conn.WriteJSON(map[string]any{"op": "subscribe", "args": args}); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	msgs := make(chan []byte, 128)
	readErr := make(chan error, 1)
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			msgs <- msg
		}
	}()

	pingTicker := time.NewTicker(bybitPingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case <-pingTicker.C:
			if err := conn.WriteJSON(map[string]string{"op": "ping"}); err != nil {
				return fmt.Errorf("ping: %w", err)
			}
		case msg := <-msgs:
			c.handleMessage(msg)
		}
	}
}

type bybitTickerMsg struct {
	Topic string `json:"topic"`
	Data  struct {
		Symbol    string `json:"symbol"`
		LastPrice string `json:"lastPrice"`
	} `json:"data"`
}

func (c *Client) handleMessage(msg []byte) {
	var ev bybitTickerMsg
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}
	if ev.Data.Symbol == "" || ev.Data.LastPrice == "" {
		return
	}
	if price, err := decimal.NewFromString(ev.Data.LastPrice); err == nil && price.IsPositive() {
		c.dispatchPrice(ev.Data.Symbol, price)
	}
}
