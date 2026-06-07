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
// onBalance is ignored — OKX does not push balance updates on the orders channel.
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

func (c *Client) streamOrdersOnce(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
) error {
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

	// OKX processes each arg independently in a single subscribe message.
	// "orders" supports instType:ANY; "orders-algo" rejects ANY (error 60018)
	// and requires an explicit instType. Subscribe to both SPOT and SWAP so
	// the same connection covers spot OCO brackets and futures algo orders.
	algoInstTypes := []string{"SPOT", "SWAP"}
	args := []map[string]string{
		{"channel": "orders", "instType": "ANY"},
	}
	for _, t := range algoInstTypes {
		args = append(args, map[string]string{"channel": "orders-algo", "instType": t})
	}
	sub := map[string]any{"op": "subscribe", "args": args}
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
			handleOKXMessage(msg, onLifecycle, onFill)
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
		OrdId      string `json:"ordId"`
		ClOrdId    string `json:"clOrdId"`
		AlgoId     string `json:"algoId"` // set when this order was triggered by an OCO/conditional algo
		InstId     string `json:"instId"`
		Side       string `json:"side"`
		State      string `json:"state"`
		Sz         string `json:"sz"`
		FillSz     string `json:"fillSz"`
		FillPx     string `json:"fillPx"`
		TradeId    string `json:"tradeId"`    // exchange trade/fill ID — unique per partial fill execution
		FillFee    string `json:"fillFee"`    // fee for this fill (negative = cost); e.g. "-0.0001507"
		FillFeeCcy string `json:"fillFeeCcy"` // fee currency; e.g. "ETH", "USDT"
		UTime      string `json:"uTime"`
	} `json:"data"`
}

// okxAlgoOrderEvent is the WS push data for the "orders-algo" channel.
// OKX fires this when an OCO/conditional algo order changes state.
type okxAlgoOrderEvent struct {
	Arg struct {
		Channel string `json:"channel"`
	} `json:"arg"`
	Data []struct {
		AlgoId     string `json:"algoId"`
		InstId     string `json:"instId"`
		Side       string `json:"side"`
		OrdType    string `json:"ordType"`    // oco | conditional
		State      string `json:"state"`      // live | effective | partially_effective | canceled | order_failed
		Sz         string `json:"sz"`         // original order size
		ActualSz   string `json:"actualSz"`   // filled size (when effective/partially_effective)
		ActualPx   string `json:"actualPx"`   // fill price (when effective/partially_effective)
		ActualSide string `json:"actualSide"` // "sl" or "tp" — which leg triggered
		UTime      string `json:"uTime"`
	} `json:"data"`
}

func handleOKXMessage(
	msg []byte,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
) {
	var ev okxOrderEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}
	switch ev.Arg.Channel {
	case "orders-algo":
		handleOKXAlgoMessage(msg, onLifecycle, onFill)
		return
	case "orders":
		// handled below
	default:
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

		orderID := d.InstId + ":" + d.OrdId // matches PlaceOrder ID format
		// When OKX triggers an OCO/conditional algo, it creates a child market order
		// with a new ordId. The runtime tracks the OCO by its algo ID ("INSTID:A:algoId"),
		// not the child order ID. Override orderID so the fill routes to the correct hand
		// and isBracketExit detection in applyFill finds the exitLevels entry.
		// The orders-algo channel fires a concurrent "effective" event with the same algo
		// ID — seenFills dedup in handleWsFill prevents double-apply.
		if d.AlgoId != "" {
			orderID = d.InstId + ":A:" + d.AlgoId
		}

		switch d.State {
		case "live":
			if onLifecycle != nil {
				onLifecycle(exchange.OrderLifecycleEvent{
					Type:          exchange.OrderLifecycleEventLive,
					OrderID:       orderID,
					ClientOrderID: d.ClOrdId,
					Symbol:        d.InstId,
					Side:          side,
					Qty:           parseDecimal(d.Sz),
					Timestamp:     ts,
				})
			}
		case "partially_filled", "filled":
			if onFill == nil {
				continue
			}
			filledQty := parseDecimal(d.FillSz)
			if !filledQty.IsPositive() {
				continue
			}
			tradeID := d.TradeId
			if tradeID == "" {
				tradeID = orderID + "_" + d.UTime // fallback (e.g. state=filled with no fill data)
			}
			fee := parseDecimal(d.FillFee)
			if fee.IsNegative() {
				fee = fee.Neg() // OKX sends fee as negative cost; normalise to positive
			}
			onFill(exchange.WsFillEvent{
				OrderID:         orderID,
				ClientOrderID:   d.ClOrdId,
				TradeID:         tradeID,
				Symbol:          d.InstId,
				Side:            side,
				Partial:         d.State == "partially_filled",
				FilledQty:       filledQty,
				FilledAvg:       parseDecimal(d.FillPx),
				Commission:      fee,
				CommissionAsset: d.FillFeeCcy,
				Timestamp:       ts,
			})
		case "canceled", "mmp_canceled":
			if onLifecycle != nil {
				onLifecycle(exchange.OrderLifecycleEvent{
					Type:          exchange.OrderLifecycleEventCanceled,
					OrderID:       orderID,
					ClientOrderID: d.ClOrdId,
					Symbol:        d.InstId,
					Side:          side,
					Qty:           parseDecimal(d.Sz),
					Timestamp:     ts,
				})
			}
		}
	}
}

// handleOKXAlgoMessage processes "orders-algo" WS channel events.
//
// OKX algo orders (OCO/conditional) have a lifecycle separate from regular orders:
//
//   - "effective" / "partially_effective" — one leg triggered and executed.
//     actualSz/actualPx carry the fill; synthesise a WsFillEvent using the algo
//     order ID ("INSTID:A:algoId") so the runtime's exitLevels lookup finds it
//     and detects isBracketExit correctly.
//
//   - "canceled" — the order was cancelled. May be:
//     (a) helm-initiated (via CancelOrder → cancel-algos);
//     (b) OCO sibling cancel (exchange auto-cancels the surviving leg after the
//     other fills — arrives shortly after the "effective" fill event);
//     (c) user manual cancel.
//     HandleExitOrderCanceled distinguishes (a)/(b) from (c) via pendingCancels.
//     Because fills drain before exitCancelCh in the run loop, the "effective"
//     fill is always processed first, populating pendingCancels, so (b) is
//     recognised as helm-initiated when it arrives.
func handleOKXAlgoMessage(
	msg []byte,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
) {
	var ev okxAlgoOrderEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}
	for _, d := range ev.Data {
		// Use ":A:" infix — must match the format written by PlaceExitOrders.
		orderID := d.InstId + ":A:" + d.AlgoId
		side := exchange.Buy
		if d.Side == "sell" {
			side = exchange.Sell
		}
		ts := time.Now().UTC()
		if ms, err := strconv.ParseInt(d.UTime, 10, 64); err == nil && ms > 0 {
			ts = time.UnixMilli(ms).UTC()
		}

		switch d.State {
		case "effective", "partially_effective":
			// One leg of the OCO/conditional triggered and executed.
			actualQty := parseDecimal(d.ActualSz)
			actualPx := parseDecimal(d.ActualPx)
			if !actualQty.IsPositive() || !actualPx.IsPositive() {
				slog.Warn("okx: algo order effective but actualSz/actualPx missing",
					"algo_id", d.AlgoId, "inst_id", d.InstId, "state", d.State)
				continue
			}
			slog.Info("okx: algo order triggered",
				"algo_id", d.AlgoId, "inst_id", d.InstId,
				"actual_side", d.ActualSide, "qty", actualQty, "px", actualPx)
			if onFill != nil {
				onFill(exchange.WsFillEvent{
					OrderID:   orderID,
					TradeID:   d.AlgoId + "_algo", // synthetic; avoids collision with regular fill IDs
					Symbol:    d.InstId,
					Side:      side,
					FilledQty: actualQty,
					FilledAvg: actualPx,
					Timestamp: ts,
					// Commission not available in orders-algo channel.
				})
			}

		case "canceled":
			slog.Info("okx: algo order canceled", "algo_id", d.AlgoId, "inst_id", d.InstId)
			if onLifecycle != nil {
				onLifecycle(exchange.OrderLifecycleEvent{
					Type:      exchange.OrderLifecycleEventCanceled,
					OrderID:   orderID,
					Symbol:    d.InstId,
					Side:      side,
					Timestamp: ts,
				})
			}
		}
	}
}
