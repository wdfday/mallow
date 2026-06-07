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
// topic, and calls handlers on each event. Reconnects automatically.
// onBalance is ignored — Bybit wallet updates arrive on a separate topic not yet subscribed.
func (c *Client) StreamOrders(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	_ func(exchange.BalanceEvent),
) error {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic recovered", "recover", r)
			}
		}()
		bo := exchange.Backoff{Min: 2 * time.Second, Max: 60 * time.Second, Factor: 2.0, Jitter: true}
		attempt := 0
		for {
			if ctx.Err() != nil {
				return
			}
			start := time.Now()
			err := c.streamOrdersOnce(ctx, creds, onLifecycle, onFill)
			if ctx.Err() != nil {
				return
			}
			if time.Since(start) > 30*time.Second {
				attempt = 0
			}
			sleepDur := bo.Next(attempt)
			attempt++
			slog.Warn("bybit: order stream disconnected, reconnecting", "err", err, "attempt", attempt, "sleep_dur", sleepDur)
			select {
			case <-time.After(sleepDur):
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("bybit: order streaming started")
	return nil
}

func (c *Client) streamOrdersOnce(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
) error {
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
			slog.Info("bybit: raw ws message", "raw", string(msg))
			c.handleBybitMessage(msg, onLifecycle, onFill)
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

func (c *Client) handleBybitMessage(
	msg []byte,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
) {
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
			if onLifecycle != nil {
				onLifecycle(exchange.OrderLifecycleEvent{
					Type:          exchange.OrderLifecycleEventLive,
					OrderID:       d.OrderID,
					ClientOrderID: d.OrderLinkId,
					Symbol:        d.Symbol,
					Side:          side,
					Qty:           parseDecimal(d.Qty),
					Timestamp:     ts,
				})
			}
		case "Trade":
			if onFill == nil {
				continue
			}
			execQty := parseDecimal(d.ExecQty)
			if !execQty.IsPositive() {
				continue
			}
			tradeID := d.ExecId
			if tradeID == "" {
				tradeID = d.OrderID + "_" + d.UpdatedTime
			}
			onFill(exchange.WsFillEvent{
				OrderID:         d.OrderID,
				ClientOrderID:   d.OrderLinkId,
				TradeID:         tradeID,
				Symbol:          d.Symbol,
				Side:            side,
				Partial:         d.OrderStatus != "Filled",
				FilledQty:       execQty,
				FilledAvg:       parseDecimal(d.ExecPrice),
				Commission:      parseDecimal(d.ExecFee),
				CommissionAsset: d.FeeCurrency,
				Timestamp:       ts,
			})
		default:
			if (d.OrderStatus == "Cancelled" || d.OrderStatus == "Rejected") && onLifecycle != nil {
				onLifecycle(exchange.OrderLifecycleEvent{
					Type:          exchange.OrderLifecycleEventCanceled,
					OrderID:       d.OrderID,
					ClientOrderID: d.OrderLinkId,
					Symbol:        d.Symbol,
					Side:          side,
					Qty:           parseDecimal(d.Qty),
					Timestamp:     ts,
				})
			}
		}
	}
}
