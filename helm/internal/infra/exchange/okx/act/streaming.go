package act

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

	"mallow/helm/internal/infra/exchange"
)

const (
	okxPrivateWSURL     = "wss://ws.okx.com:8443/ws/v5/private"
	okxPrivateWSDemoURL = "wss://wspap.okx.com:8443/ws/v5/private"
	okxPingInterval     = 25 * time.Second
)

// StreamOrders implements exchange.AccountStreamer.
// balanceHandler is ignored — OKX does not push balance updates on the orders channel.
func (c *Client) StreamOrders(ctx context.Context, creds exchange.Credentials, handler func(exchange.OrderEvent), _ func(exchange.BalanceEvent)) error {
	go func() {
		bo := exchange.Backoff{Min: 2 * time.Second, Max: 60 * time.Second, Factor: 2.0, Jitter: true}
		attempt := 0
		for {
			if ctx.Err() != nil {
				return
			}
			start := time.Now()
			err := c.streamOrdersOnce(ctx, creds, handler)
			if ctx.Err() != nil {
				return
			}
			if time.Since(start) > 30*time.Second {
				attempt = 0
			}
			sleepDur := bo.Next(attempt)
			attempt++
			slog.Warn("okx: order stream disconnected, reconnecting", "err", err, "attempt", attempt, "sleep_dur", sleepDur)
			select {
			case <-time.After(sleepDur):
			case <-ctx.Done():
				return
			}
		}
	}()
	slog.Info("okx: order streaming started")
	return nil
}

func (c *Client) streamOrdersOnce(ctx context.Context, creds exchange.Credentials, handler func(exchange.OrderEvent)) error {
	wsURL := okxPrivateWSURL
	if c.paper {
		wsURL = okxPrivateWSDemoURL
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	if err := wsLogin(conn, creds); err != nil {
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
			slog.Info("okx: raw ws message", "raw", string(msg))
			handleOKXMessage(msg, handler)
		}
	}
}

func wsLogin(conn *websocket.Conn, creds exchange.Credentials) error {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sign := okxWsSign(ts, creds.APISecret)

	loginMsg := map[string]any{
		"op": "login",
		"args": []map[string]string{{
			"apiKey":     creds.APIKey,
			"passphrase": creds.Passphrase,
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
		State  string `json:"state"`
		Sz     string `json:"sz"`
		FillSz string `json:"fillSz"`
		FillPx string `json:"fillPx"`
		FillId string `json:"fillId"` // exchange fill ID — unique per partial fill
		UTime  string `json:"uTime"`
	} `json:"data"`
}

func handleOKXMessage(msg []byte, handler func(exchange.OrderEvent)) {
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

		orderID := d.InstId + ":" + d.OrdId // matches PlaceOrder ID format
		tradeID := orderID + "_open"
		switch evType {
		case exchange.OrderEventPartialFill, exchange.OrderEventFilled:
			if d.FillId != "" {
				tradeID = d.FillId
			} else {
				tradeID = orderID + "_" + d.UTime // fallback
			}
		case exchange.OrderEventCanceled:
			tradeID = orderID + "_cancel"
		}

		oe := exchange.OrderEvent{
			Type:      evType,
			OrderID:   orderID,
			TradeID:   tradeID,
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
