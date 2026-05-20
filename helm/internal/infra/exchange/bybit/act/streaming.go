package act

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

	"mallow/helm/internal/infra/exchange"
)

const (
	bybitPrivateWSURL      = "wss://stream.bybit.com/v5/private"
	bybitPrivateWSPaperURL = "wss://stream-demo.bybit.com/v5/private"
	bybitWsPingInterval    = 20 * time.Second
)

// StreamOrders implements exchange.AccountStreamer.
// Connects to Bybit private WebSocket, authenticates, subscribes to the "order"
// topic, and calls handler on each order lifecycle event. Reconnects automatically.
// balanceHandler is ignored — Bybit wallet updates arrive on a separate topic not yet subscribed.
func (c *Client) StreamOrders(ctx context.Context, creds exchange.Credentials, handler func(exchange.OrderEvent), _ func(exchange.BalanceEvent)) error {
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			err := c.streamOrdersOnce(ctx, creds, handler)
			if ctx.Err() != nil {
				return
			}
			slog.Warn("bybit: order stream disconnected, reconnecting in 5s", "err", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("bybit: order streaming started")
	return nil
}

func (c *Client) streamOrdersOnce(ctx context.Context, creds exchange.Credentials, handler func(exchange.OrderEvent)) error {
	wsURL := bybitPrivateWSURL
	if c.paper {
		wsURL = bybitPrivateWSPaperURL
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	if err := wsAuth(conn, creds); err != nil {
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
func wsAuth(conn *websocket.Conn, creds exchange.Credentials) error {
	expires := strconv.FormatInt(time.Now().UnixMilli()+1000, 10)
	preSign := "GET/realtime" + expires
	mac := hmac.New(sha256.New, []byte(creds.APISecret))
	mac.Write([]byte(preSign))
	sig := hex.EncodeToString(mac.Sum(nil))

	authMsg := map[string]any{
		"op":   "auth",
		"args": []string{creds.APIKey, expires, sig},
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

func (c *Client) handleBybitMessage(msg []byte, handler func(exchange.OrderEvent)) {
	var ev bybitOrderEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}
	if ev.Topic != "order" {
		return
	}
	for _, d := range ev.Data {
		side := exchange.Buy
		if strings.ToLower(d.Side) == "sell" {
			side = exchange.Sell
		}
		ts := time.Now().UTC()
		if ms, err := strconv.ParseInt(d.UpdatedTime, 10, 64); err == nil && ms > 0 {
			ts = time.UnixMilli(ms).UTC()
		}

		switch d.ExecType {
		case "New":
			handler(exchange.OrderEvent{
				Type:      exchange.OrderEventLive,
				OrderID:   d.OrderID,
				TradeID:   d.OrderID + "_open",
				Symbol:    d.Symbol,
				Side:      side,
				Qty:       parseDecimal(d.Qty),
				Timestamp: ts,
			})
		case "Trade":
			evType := exchange.OrderEventPartialFill
			if d.OrderStatus == "Filled" {
				evType = exchange.OrderEventFilled
			}
			execQty := parseDecimal(d.ExecQty)
			if !execQty.IsPositive() {
				continue
			}
			tradeID := d.ExecId
			if tradeID == "" {
				tradeID = d.OrderID + "_" + d.UpdatedTime
			}
			handler(exchange.OrderEvent{
				Type:      evType,
				OrderID:   d.OrderID,
				TradeID:   tradeID,
				Symbol:    d.Symbol,
				Side:      side,
				Qty:       parseDecimal(d.Qty),
				FilledQty: execQty,
				FilledAvg: parseDecimal(d.ExecPrice),
				Timestamp: ts,
			})
		default:
			if d.OrderStatus == "Cancelled" || d.OrderStatus == "Rejected" {
				handler(exchange.OrderEvent{
					Type:      exchange.OrderEventCanceled,
					OrderID:   d.OrderID,
					TradeID:   d.OrderID + "_cancel",
					Symbol:    d.Symbol,
					Side:      side,
					Qty:       parseDecimal(d.Qty),
					Timestamp: ts,
				})
			}
		}
	}
}
