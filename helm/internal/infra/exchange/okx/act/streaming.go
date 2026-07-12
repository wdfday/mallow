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
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"mallow/helm/internal/infra/exchange"
)

const (
	okxPrivateWSURL     = "wss://ws.okx.com:8443/ws/v5/private"
	okxPrivateWSDemoURL = "wss://wspap.okx.com:8443/ws/v5/private"
	// The "orders-algo" channel (OCO/conditional bracket status pushes) migrated to a
	// separate business endpoint — subscribing to it on /private is rejected with
	// "Wrong URL or channel" (error 60018), which is exactly what it says: wrong URL,
	// not a bad param. This was silently broken (subscribed on /private, always
	// rejected) until 2026-07-13 — no live WS notification for external bracket
	// cancels/triggers ever reached the hand; only startup reconcile (RecoverBrackets)
	// caught it, via REST GetOrder, not WS.
	okxBusinessWSURL     = "wss://ws.okx.com:8443/ws/v5/business"
	okxBusinessWSDemoURL = "wss://wspap.okx.com:8443/ws/v5/business"
	okxPingInterval      = 25 * time.Second
)

// isPermanentOKXError returns true for login errors that won't self-heal on retry —
// bad key, bad sign, bad timestamp, revoked key. These stop the reconnect loop instead
// of retrying forever. Codes come from wsLogin's "login failed: code=%s msg=%s" wrap.
//
// 50111/50113/50114/50119 are REST-API error codes; the WS login channel uses a
// separate numbering (60xxx) for the same failure classes — e.g. a revoked/nonexistent
// key comes back as code=60032 msg="API key doesn't exist" on WS, not 50119. Without
// 60032 here, a dead OKX key retries forever (capped backoff) instead of firing
// onCredentialError, so the helm never flips to status=error.
func isPermanentOKXError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "code=50111") || // Invalid API Key (REST)
		strings.Contains(s, "code=50113") || // Invalid sign (REST)
		strings.Contains(s, "code=50114") || // Invalid authorization (REST)
		strings.Contains(s, "code=50119") || // API key doesn't exist (REST)
		strings.Contains(s, "code=60032") // API key doesn't exist (WS)
}

// StreamOrders implements exchange.AccountStreamer.
// onBalance is ignored — OKX does not push balance updates on the orders channel.
// onCredentialError is called (once) when isPermanentOKXError classifies the login
// failure as unrecoverable; the reconnect loop stops afterward (see AccountStreamer doc).
func (c *Client) StreamOrders(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	_ func(exchange.BalanceEvent),
	onPosition func(exchange.PositionEvent),
	onRisk func(exchange.RiskEvent),
	onCredentialError func(string),
) error {
	c.runOKXReconnectLoop(ctx, "order", onCredentialError, func() error {
		return c.streamOrdersOnce(ctx, creds, onLifecycle, onFill, onPosition, onRisk)
	})
	slog.Info("okx: order streaming started")

	// Separate connection: "orders-algo" (OCO/conditional bracket status pushes) lives
	// on the business endpoint, not /private — see okxBusinessWSURL comment. Runs its
	// own independent reconnect loop so a drop on one connection never takes down the
	// other; either can hit isPermanentOKXError on the same shared credentials.
	c.runOKXReconnectLoop(ctx, "algo order", onCredentialError, func() error {
		return c.streamAlgoOrdersOnce(ctx, creds, onLifecycle, onFill)
	})
	slog.Info("okx: algo order streaming started")
	return nil
}

// runOKXReconnectLoop launches the shared dial/backoff/reconnect goroutine used by
// both the order stream (/private) and the algo-order stream (/business) — same
// backoff policy, same permanent-error handling, only the connect function differs.
func (c *Client) runOKXReconnectLoop(
	ctx context.Context,
	label string,
	onCredentialError func(string),
	connectOnce func() error,
) {
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
			err := connectOnce()
			if ctx.Err() != nil {
				return
			}
			if time.Since(start) > 30*time.Second {
				attempt = 0
			}
			if isPermanentOKXError(err) {
				slog.Error("okx: permanent stream error — stopping reconnect loop", "stream", label, "err", err)
				if onCredentialError != nil {
					onCredentialError(fmt.Errorf("okx WS %s stream: %w", label, err).Error())
				}
				return
			}
			sleepDur := bo.Next(attempt)
			attempt++
			slog.Warn("okx: stream disconnected, reconnecting", "stream", label, "err", err, "attempt", attempt, "sleep_dur", sleepDur)
			select {
			case <-time.After(sleepDur):
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (c *Client) streamOrdersOnce(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	onPosition func(exchange.PositionEvent),
	onRisk func(exchange.RiskEvent),
) error {
	wsURL := okxPrivateWSURL
	if c.paper {
		wsURL = okxPrivateWSDemoURL
	}
	// "orders" and "positions" both support instType:ANY on the private endpoint.
	args := []map[string]string{
		{"channel": "orders", "instType": "ANY"},
		{"channel": "positions", "instType": "ANY"}, // futures position updates
	}
	return c.runOKXWSConn(ctx, wsURL, creds, args, func(msg []byte) {
		handleOKXMessage(msg, onLifecycle, onFill, onPosition, onRisk)
	})
}

// streamAlgoOrdersOnce connects to the business endpoint for "orders-algo" (OCO/
// conditional bracket status pushes: triggered, cancelled, etc). Separate connection
// from streamOrdersOnce — see okxBusinessWSURL comment for why.
func (c *Client) streamAlgoOrdersOnce(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
) error {
	wsURL := okxBusinessWSURL
	if c.paper {
		wsURL = okxBusinessWSDemoURL
	}
	// "orders-algo" rejects instType:ANY (error 60018) — requires an explicit
	// instType. Subscribe to both SPOT and SWAP so this one connection covers spot
	// OCO brackets and futures conditional orders.
	args := []map[string]string{
		{"channel": "orders-algo", "instType": "SPOT"},
		{"channel": "orders-algo", "instType": "SWAP"},
	}
	return c.runOKXWSConn(ctx, wsURL, creds, args, func(msg []byte) {
		handleOKXAlgoMessage(msg, onLifecycle, onFill)
	})
}

// runOKXWSConn is the shared dial → login → subscribe → ping → read-dispatch loop
// used by both streamOrdersOnce (/private) and streamAlgoOrdersOnce (/business).
// dispatch is called with every non-pong message; the caller supplies the right
// handler (handleOKXMessage or handleOKXAlgoMessage) for its channel(s).
func (c *Client) runOKXWSConn(
	ctx context.Context,
	wsURL string,
	creds exchange.Credentials,
	subscribeArgs []map[string]string,
	dispatch func(msg []byte),
) error {
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	if err := wsLogin(conn, creds); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	sub := map[string]any{"op": "subscribe", "args": subscribeArgs}
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
			dispatch(msg)
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

type okxPositionEvent struct {
	Arg struct {
		Channel string `json:"channel"`
	} `json:"arg"`
	Data []struct {
		InstId   string `json:"instId"`
		PosSide  string `json:"posSide"`  // net/long/short
		Pos      string `json:"pos"`      // position size (signed)
		AvgPx    string `json:"avgPx"`    // average entry price
		Upl      string `json:"upl"`      // unrealized P&L
		MgnRatio string `json:"mgnRatio"` // margin ratio; empty for cross/no-margin
		LiqPx    string `json:"liqPx"`    // estimated liquidation price; "0" if not applicable
		UTime    string `json:"uTime"`
	} `json:"data"`
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
	onPosition func(exchange.PositionEvent),
	onRisk func(exchange.RiskEvent),
) {
	var ev okxOrderEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}
	switch ev.Arg.Channel {
	case "orders-algo":
		handleOKXAlgoMessage(msg, onLifecycle, onFill)
		return
	case "positions":
		handleOKXPositionMessage(msg, onPosition, onRisk)
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

// handleOKXPositionMessage processes "positions" WS channel events.
// Emits PositionEvent for each position update, and RiskEvent when margin ratio or
// liquidation price is non-zero (indicating the position has margin risk data).
func handleOKXPositionMessage(msg []byte, onPosition func(exchange.PositionEvent), onRisk func(exchange.RiskEvent)) {
	var ev okxPositionEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		return
	}
	for _, d := range ev.Data {
		ts := time.Now().UTC()
		if ms, err := strconv.ParseInt(d.UTime, 10, 64); err == nil && ms > 0 {
			ts = time.UnixMilli(ms).UTC()
		}
		if onPosition != nil {
			side := exchange.PositionNet
			switch d.PosSide {
			case "long":
				side = exchange.PositionLong
			case "short":
				side = exchange.PositionShort
			}
			onPosition(exchange.PositionEvent{
				Symbol:        d.InstId,
				Side:          side,
				Size:          parseDecimal(d.Pos),
				EntryPrice:    parseDecimal(d.AvgPx),
				UnrealizedPnL: parseDecimal(d.Upl),
				At:            ts,
			})
		}
		if onRisk != nil {
			mgnRatio := parseDecimal(d.MgnRatio)
			liqPx := parseDecimal(d.LiqPx)
			if mgnRatio.IsPositive() || liqPx.IsPositive() {
				onRisk(exchange.RiskEvent{
					Symbol:           d.InstId,
					MarginRatio:      mgnRatio,
					LiquidationPrice: liqPx,
					At:               ts,
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
