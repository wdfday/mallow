package action

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"orchestrator/internal/infra/exchange"
)

const (
	bybitPrivateWSURL        = "wss://stream.bybit.com/v5/private"
	bybitPrivateWSTestnetURL = "wss://stream-testnet.bybit.com/v5/private"
	bybitWsPingInterval      = 20 * time.Second
)

// StreamFills implements exchange.AccountStreamer.
// Connects to Bybit private WebSocket, authenticates, subscribes to the "order"
// topic, and calls handler on each fill. Reconnects automatically on disconnection.
func (c *Client) StreamFills(ctx context.Context, handler func(exchange.FillEvent)) error {
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			err := c.streamFillsOnce(ctx, handler)
			if ctx.Err() != nil {
				return
			}
			slog.Warn("bybit: fill stream disconnected, reconnecting in 5s", "err", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("bybit: fill streaming started")
	return nil
}

func (c *Client) streamFillsOnce(ctx context.Context, handler func(exchange.FillEvent)) error {
	wsURL := bybitPrivateWSURL
	if c.cfg.Testnet {
		wsURL = bybitPrivateWSTestnetURL
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	if err := c.wsAuth(conn); err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	sub := map[string]any{
		"op":   "subscribe",
		"args": []string{"order"},
	}
	if err := conn.WriteJSON(sub); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	msgs := make(chan []byte, 64)
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

	pingTicker := time.NewTicker(bybitWsPingInterval)
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
			c.handleBybitMessage(msg, handler)
		}
	}
}

// wsAuth sends the Bybit private WS authentication message.
// Signature: HMAC-SHA256("GET/realtime" + expires, apiSecret)
func (c *Client) wsAuth(conn *websocket.Conn) error {
	expires := strconv.FormatInt(time.Now().UnixMilli()+1000, 10)
	preSign := "GET/realtime" + expires
	mac := hmac.New(sha256.New, []byte(c.cfg.APISecret))
	mac.Write([]byte(preSign))
	sig := hex.EncodeToString(mac.Sum(nil))

	authMsg := map[string]any{
		"op":   "auth",
		"args": []string{c.cfg.APIKey, expires, sig},
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	_, resp, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}

	var authResp struct {
		Op      string `json:"op"`
		Success bool   `json:"success"`
		RetMsg  string `json:"ret_msg"`
	}
	if err := json.Unmarshal(resp, &authResp); err != nil {
		return fmt.Errorf("parse auth response: %w", err)
	}
	if !authResp.Success {
		return fmt.Errorf("auth failed: %s", authResp.RetMsg)
	}
	return nil
}

type bybitOrderEvent struct {
	Topic string `json:"topic"`
	Data  []struct {
		OrderID   string `json:"orderId"`
		Symbol    string `json:"symbol"`
		Side      string `json:"side"`     // "Buy" | "Sell"
		ExecType  string `json:"execType"` // "Trade" on fills
		ExecQty   string `json:"execQty"`
		ExecPrice string `json:"execPrice"`
		TradeTime int64  `json:"tradeTime"`
	} `json:"data"`
}

func (c *Client) handleBybitMessage(msg []byte, handler func(exchange.FillEvent)) {
	var ev bybitOrderEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}
	if ev.Topic != "order" {
		return
	}
	for _, d := range ev.Data {
		if d.ExecType != "Trade" {
			continue
		}
		qty := parseDecimal(d.ExecQty)
		if !qty.IsPositive() {
			continue
		}
		side := exchange.Buy
		if strings.ToLower(d.Side) == "sell" {
			side = exchange.Sell
		}
		handler(exchange.FillEvent{
			OrderID:   d.OrderID,
			Symbol:    d.Symbol,
			Side:      side,
			FilledQty: qty,
			FilledAvg: parseDecimal(d.ExecPrice),
			Timestamp: time.UnixMilli(d.TradeTime).UTC(),
		})
	}
}
