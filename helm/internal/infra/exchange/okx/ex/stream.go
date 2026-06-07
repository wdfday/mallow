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
	okxPublicWSURL     = "wss://ws.okx.com:8443/ws/v5/public"
	okxPublicWSDemoURL = "wss://wspap.okx.com:8443/ws/v5/public?brokerId=9999"
	okxPingInterval    = 25 * time.Second
)

// Subscribe connects to the OKX public WebSocket and subscribes to both
// "trades" (last price) and "tickers" (best bid/offer) channels for all
// given symbols. One shared connection serves all registered handlers.
// Reconnects automatically on disconnection. Blocks until ctx is cancelled.
func (c *Client) Subscribe(ctx context.Context, symbols []string) error {
	go c.run(ctx, symbols)
	<-ctx.Done()
	return ctx.Err()
}

func (c *Client) run(ctx context.Context, symbols []string) {
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.runOnce(ctx, symbols)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("okx/ex: market stream disconnected, reconnecting in 3s", "err", err)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) runOnce(ctx context.Context, symbols []string) error {
	wsURL := okxPublicWSURL
	if c.paper {
		wsURL = okxPublicWSDemoURL
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// Subscribe to trades (last price), tickers (BBO), and books5 (L2 top-5).
	args := make([]map[string]string, 0, len(symbols)*3)
	for _, sym := range symbols {
		args = append(args,
			map[string]string{"channel": "trades", "instId": sym},
			map[string]string{"channel": "tickers", "instId": sym},
			map[string]string{"channel": "books5", "instId": sym},
		)
	}
	if err := conn.WriteJSON(map[string]any{"op": "subscribe", "args": args}); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	msgs := make(chan []byte, 128)
	readErr := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic recovered", "recover", r)
			}
		}()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				readErr <- err
				return
			}
			msgs <- msg
		}
	}()

	pingTicker := time.NewTicker(okxPingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			return err
		case <-pingTicker.C:
			if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
				return fmt.Errorf("ping: %w", err)
			}
		case msg := <-msgs:
			if string(msg) == "pong" {
				continue
			}
			c.handleMessage(msg)
		}
	}
}

// ── Event types ───────────────────────────────────────────────────────────────

type okxChannelEvent struct {
	Arg struct {
		Channel string `json:"channel"`
		InstID  string `json:"instId"`
	} `json:"arg"`
	Data json.RawMessage `json:"data"`
}

type okxTradeData struct {
	InstID string `json:"instId"`
	Px     string `json:"px"`
}

type okxTickerData struct {
	BidPx string `json:"bidPx"`
	AskPx string `json:"askPx"`
}

type okxBooks5Data struct {
	Bids [][]string `json:"bids"` // [[price, size, _, orders], ...]
	Asks [][]string `json:"asks"`
}

func (c *Client) handleMessage(msg []byte) {
	var ev okxChannelEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}

	switch ev.Arg.Channel {
	case "trades":
		var trades []okxTradeData
		if err := json.Unmarshal(ev.Data, &trades); err != nil {
			return
		}
		for _, d := range trades {
			if price, err := decimal.NewFromString(d.Px); err == nil && price.IsPositive() {
				c.dispatchPrice(d.InstID, price)
			}
		}

	case "tickers":
		var tickers []okxTickerData
		if err := json.Unmarshal(ev.Data, &tickers); err != nil {
			return
		}
		for _, d := range tickers {
			bid := parseFloat(d.BidPx)
			ask := parseFloat(d.AskPx)
			if bid > 0 && ask > 0 {
				c.dispatchQuote(ev.Arg.InstID, bid, ask)
			}
		}

	case "books5":
		var books []okxBooks5Data
		if err := json.Unmarshal(ev.Data, &books); err != nil {
			return
		}
		for _, d := range books {
			book := parseL2Book(d)
			c.dispatchBook(ev.Arg.InstID, book)
		}
	}
}

func parseL2Book(d okxBooks5Data) L2Book {
	var book L2Book
	for i := 0; i < 5 && i < len(d.Bids); i++ {
		book.Bids[i] = Level{Price: parseFloat(d.Bids[i][0]), Size: parseFloat(d.Bids[i][1])}
	}
	for i := 0; i < 5 && i < len(d.Asks); i++ {
		book.Asks[i] = Level{Price: parseFloat(d.Asks[i][0]), Size: parseFloat(d.Asks[i][1])}
	}
	return book
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}
