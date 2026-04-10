package action

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/gorilla/websocket"

	"orchestrator/internal/infra/exchange"
)

const (
	okxPrivateWSURL     = "wss://ws.okx.com:8443/ws/v5/private"
	okxPrivateWSDemoURL = "wss://wspap.okx.com:8443/ws/v5/private"
	okxPingInterval     = 25 * time.Second
)

// StreamOrders implements exchange.AccountStreamer.
// Connects to OKX private WebSocket, subscribes to the "orders" channel, and
// calls handler on each order lifecycle event. Reconnects automatically on disconnection.
func (c *Client) StreamOrders(ctx context.Context, handler func(exchange.OrderEvent)) error {
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			err := c.streamOrdersOnce(ctx, handler)
			if ctx.Err() != nil {
				return
			}
			slog.Warn("okx: order stream disconnected, reconnecting in 5s", "err", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("okx: order streaming started")
	return nil
}

func (c *Client) streamOrdersOnce(ctx context.Context, handler func(exchange.OrderEvent)) error {
	wsURL := okxPrivateWSURL
	if c.cfg.Demo {
		wsURL = okxPrivateWSDemoURL
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	if err := c.wsLogin(conn); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	sub := map[string]any{
		"op":   "subscribe",
		"args": []map[string]string{{"channel": "orders", "instType": "ANY"}},
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
			c.handleOKXMessage(msg, handler)
		}
	}
}

func (c *Client) wsLogin(conn *websocket.Conn) error {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sign := okxWsSign(ts, c.cfg.APISecret)

	loginMsg := map[string]any{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     c.cfg.APIKey,
			"passphrase": c.cfg.Passphrase,
			"timestamp":  ts,
			"sign":       sign,
		}},
	}
	if err := conn.WriteJSON(loginMsg); err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	_, resp, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read login response: %w", err)
	}

	var loginResp struct {
		Event string `json:"event"`
		Code  string `json:"code"`
		Msg   string `json:"msg"`
	}
	if err := json.Unmarshal(resp, &loginResp); err != nil {
		return fmt.Errorf("parse login response: %w", err)
	}
	if loginResp.Event != "login" || loginResp.Code != "0" {
		return fmt.Errorf("login failed: code=%s msg=%s", loginResp.Code, loginResp.Msg)
	}
	return nil
}

// okxWsSign produces HMAC-SHA256 for OKX private WS auth.
func okxWsSign(timestamp, secret string) string {
	msg := timestamp + "GET" + "/users/self/verify"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

type okxOrderEvent struct {
	Arg struct {
		Channel string `json:"channel"`
	} `json:"arg"`
	Data []struct {
		OrdId  string `json:"ordId"`
		InstId string `json:"instId"`
		Side   string `json:"side"`
		State  string `json:"state"`  // "live", "partially_filled", "filled", "canceled", "mmp_canceled"
		Sz     string `json:"sz"`     // original submitted qty
		FillSz string `json:"fillSz"` // qty filled in this event
		FillPx string `json:"fillPx"` // price of this fill
		UTime  string `json:"uTime"`  // update time ms
	} `json:"data"`
}

func (c *Client) handleOKXMessage(msg []byte, handler func(exchange.OrderEvent)) {
	var ev okxOrderEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}
	if ev.Arg.Channel != "orders" {
		return
	}
	for _, d := range ev.Data {
		side := exchange.Buy
		if d.Side == "sell" {
			side = exchange.Sell
		}
		ts := time.Now().UTC()
		if ms, err := strconv.ParseInt(d.UTime, 10, 64); err == nil && ms > 0 {
			ts = time.UnixMilli(ms).UTC()
		}

		var evType exchange.OrderEventType
		switch d.State {
		case "live":
			evType = exchange.OrderEventLive
		case "partially_filled":
			evType = exchange.OrderEventPartialFill
		case "filled":
			evType = exchange.OrderEventFilled
		case "canceled", "mmp_canceled":
			evType = exchange.OrderEventCanceled
		default:
			continue
		}

		oe := exchange.OrderEvent{
			Type:      evType,
			OrderID:   d.OrdId,
			Symbol:    d.InstId,
			Side:      side,
			Qty:       parseDecimal(d.Sz),
			Timestamp: ts,
		}
		if evType == exchange.OrderEventPartialFill || evType == exchange.OrderEventFilled {
			oe.FilledQty = parseDecimal(d.FillSz)
			oe.FilledAvg = parseDecimal(d.FillPx)
			if !oe.FilledQty.IsPositive() {
				continue
			}
		}
		handler(oe)
	}
}
