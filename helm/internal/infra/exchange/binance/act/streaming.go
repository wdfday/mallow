package act

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/delivery"
	"github.com/adshao/go-binance/v2/futures"

	"mallow/helm/internal/infra/exchange"
)

// isPermanentStreamError returns true for errors that won't self-heal on retry —
// bad credentials, revoked keys, deprecated endpoints. These get max backoff immediately.
func isPermanentStreamError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "-2015") || // Invalid API-key / IP / permissions
		strings.Contains(s, "-2014") || // API-key format invalid
		strings.Contains(s, "410") || // Endpoint gone (deprecated)
		strings.Contains(s, "401") // Unauthorized
}

// streamBackoff returns the next wait duration using exponential backoff.
// Permanent errors jump straight to maxWait.
func streamBackoff(attempt int, permanent bool) time.Duration {
	const (
		base    = 5 * time.Second
		maxWait = 5 * time.Minute
	)
	if permanent {
		return maxWait
	}
	d := base * (1 << min(attempt, 6)) // cap exponent at 64×
	if d > maxWait {
		d = maxWait
	}
	return d
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// wsMu guards gobinance.UseTestnet global flag.
var wsMu sync.Mutex

// StreamOrders implements exchange.AccountStreamer.
// Dispatches to the correct WebSocket stream based on creds.AccountType:
//   - AccountFuturesUSDM → USDM futures stream (FAPI)
//   - AccountFuturesCOINM → COINM delivery stream (DAPI)
//   - AccountSpot (default) → spot user-data stream
//
// Each account type gets exactly one stream — no cross-contamination of events.
func (c *Client) StreamOrders(ctx context.Context, creds exchange.Credentials, orderHandler func(exchange.OrderEvent), balanceHandler func(exchange.BalanceEvent)) error {
	switch creds.AccountType {
	case exchange.AccountFuturesUSDM:
		go c.streamFuturesOrders(ctx, c.newFut(creds), orderHandler)
	case exchange.AccountFuturesCOINM:
		go c.streamDeliveryOrders(ctx, creds, orderHandler)
	default: // AccountSpot
		go c.streamSpotOrders(ctx, creds, orderHandler, balanceHandler)
	}
	slog.Info("binance: order streaming started", "account_type", creds.AccountType)
	return nil
}

// ── Spot ──────────────────────────────────────────────────────────────────────

func (c *Client) streamSpotOrders(ctx context.Context, creds exchange.Credentials, orderHandler func(exchange.OrderEvent), balanceHandler func(exchange.BalanceEvent)) {
	bo := exchange.Backoff{Min: 5 * time.Second, Max: 5 * time.Minute, Factor: 2.0, Jitter: true}
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := c.streamSpotOrdersOnce(ctx, creds, orderHandler, balanceHandler)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			attempt = 0
		}
		permanent := isPermanentStreamError(err)
		wait := bo.Next(attempt)
		if permanent {
			wait = 5 * time.Minute
		}
		attempt++
		slog.Warn("binance: spot order stream disconnected", "err", err, "retry_in", wait, "permanent", permanent)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

// streamSpotOrdersOnce uses WsUserDataServeSignature (HMAC, no listen key).
// WsUserDataServe (listen-key) is deprecated and returns 410 — always use signature auth.
func (c *Client) streamSpotOrdersOnce(ctx context.Context, creds exchange.Credentials, orderHandler func(exchange.OrderEvent), balanceHandler func(exchange.BalanceEvent)) error {
	wsMu.Lock()
	gobinance.UseDemo = c.paper
	doneC, stopC, err := gobinance.WsUserDataServeSignature(
		creds.APIKey, creds.APISecret, "HMAC", 0,
		spotAccountHandler(orderHandler, balanceHandler),
		func(err error) { slog.Warn("binance: spot ws error", "err", err) },
	)
	gobinance.UseDemo = false
	wsMu.Unlock()
	if err != nil {
		return fmt.Errorf("ws spot user data (signature): %w", err)
	}
	select {
	case <-ctx.Done():
		close(stopC)
		return nil
	case <-doneC:
		return fmt.Errorf("spot user stream closed")
	}
}

// spotAccountHandler dispatches both order lifecycle events and balance-change
// events arriving on the same Binance USER_DATA WebSocket stream.
func spotAccountHandler(orderHandler func(exchange.OrderEvent), balanceHandler func(exchange.BalanceEvent)) gobinance.WsUserDataHandler {
	return func(event *gobinance.WsUserDataEvent) {
		if rawJSON, err := json.Marshal(event); err == nil {
			slog.Info("binance: raw spot ws event", "raw", string(rawJSON))
		} else {
			slog.Warn("binance: failed to marshal spot ws event", "err", err)
		}
		switch event.Event {
		case gobinance.UserDataEventTypeOutboundAccountPosition:
			if balanceHandler == nil {
				return
			}
			at := time.Now().UTC()
			for _, b := range event.AccountUpdate.WsAccountUpdates {
				free := parseDecimal(b.Free)
				balanceHandler(exchange.BalanceEvent{
					Asset: b.Asset,
					Free:  free,
					At:    at,
				})
			}
			return
		case gobinance.UserDataEventTypeExecutionReport:
			// handled below
		default:
			return
		}
		ou := event.OrderUpdate
		side := exchange.Buy
		if gobinance.SideType(ou.Side) == gobinance.SideTypeSell {
			side = exchange.Sell
		}
		ts := time.UnixMilli(ou.TransactionTime).UTC()
		// Prefix with symbol to match the "SYMBOL:numericID" format used by spot
		// PlaceOrder REST responses (types.go spotCreateToResult). Without this,
		// WS fill routing via orderHandMap always misses — hand tracked the prefixed
		// key, WS looked up the raw key — causing every fill to be treated as manual.
		orderID := ou.Symbol + ":" + strconv.FormatInt(ou.Id, 10)

		switch ou.ExecutionType {
		case "NEW":
			orderHandler(exchange.OrderEvent{
				Type:      exchange.OrderEventLive,
				OrderID:   orderID,
				TradeID:   orderID + "_open",
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.Volume),
				Timestamp: ts,
			})
		case "TRADE":
			evType := exchange.OrderEventPartialFill
			if ou.Status == "FILLED" {
				evType = exchange.OrderEventFilled
			}
			qty := parseDecimal(ou.LatestVolume)
			if !qty.IsPositive() {
				return
			}
			slog.Info("binance: spot fill received",
				"order_id", orderID,
				"trade_id", ou.TradeId,
				"symbol", ou.Symbol,
				"side", side,
				"fill_qty", qty,
				"fill_avg", parseDecimal(ou.LatestPrice),
				"status", ou.Status,
				"exchange_ts", ts,
			)
			orderHandler(exchange.OrderEvent{
				Type:            evType,
				OrderID:         orderID,
				TradeID:         strconv.FormatInt(ou.TradeId, 10),
				Symbol:          ou.Symbol,
				Side:            side,
				Qty:             parseDecimal(ou.Volume),
				FilledQty:       qty,
				FilledAvg:       parseDecimal(ou.LatestPrice),
				Commission:      parseDecimal(ou.FeeCost),
				CommissionAsset: ou.FeeAsset,
				Timestamp:       ts,
			})
		case "CANCELED", "EXPIRED", "REJECTED":
			orderHandler(exchange.OrderEvent{
				Type:      exchange.OrderEventCanceled,
				OrderID:   orderID,
				TradeID:   orderID + "_cancel",
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.Volume),
				Timestamp: ts,
			})
		}
	}
}

// ── Futures ───────────────────────────────────────────────────────────────────

func (c *Client) streamFuturesOrders(ctx context.Context, fut *futures.Client, orderHandler func(exchange.OrderEvent)) {
	bo := exchange.Backoff{Min: 5 * time.Second, Max: 5 * time.Minute, Factor: 2.0, Jitter: true}
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := c.streamFuturesOrdersOnce(ctx, fut, orderHandler)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			attempt = 0
		}
		wait := bo.Next(attempt)
		attempt++
		slog.Warn("binance: futures order stream disconnected, reconnecting", "err", err, "attempt", attempt, "retry_in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) streamFuturesOrdersOnce(ctx context.Context, fut *futures.Client, orderHandler func(exchange.OrderEvent)) error {
	listenKey, err := fut.NewStartUserStreamService().Do(ctx)
	if err != nil {
		return fmt.Errorf("start futures user stream: %w", err)
	}

	wsMu.Lock()
	futures.UseDemo = c.paper
	doneC, stopC, err := futures.WsUserDataServe(listenKey, func(event *futures.WsUserDataEvent) {
		if rawJSON, err := json.Marshal(event); err == nil {
			slog.Info("binance: raw futures ws event", "raw", string(rawJSON))
		} else {
			slog.Warn("binance: failed to marshal futures ws event", "err", err)
		}
		if event.Event != futures.UserDataEventTypeOrderTradeUpdate {
			return
		}
		ou := event.OrderTradeUpdate
		side := exchange.Buy
		if ou.Side == futures.SideTypeSell {
			side = exchange.Sell
		}
		ts := time.UnixMilli(ou.TradeTime).UTC()
		orderID := strconv.FormatInt(ou.ID, 10)

		switch ou.ExecutionType {
		case "NEW":
			orderHandler(exchange.OrderEvent{
				Type:      exchange.OrderEventLive,
				OrderID:   orderID,
				TradeID:   orderID + "_open",
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.OriginalQty),
				Timestamp: ts,
			})
		case "TRADE":
			evType := exchange.OrderEventPartialFill
			if ou.Status == futures.OrderStatusTypeFilled {
				evType = exchange.OrderEventFilled
			}
			qty := parseDecimal(ou.LastFilledQty)
			if !qty.IsPositive() {
				return
			}
			slog.Info("binance: futures fill received",
				"order_id", orderID,
				"trade_id", ou.TradeID,
				"symbol", ou.Symbol,
				"side", side,
				"fill_qty", qty,
				"fill_avg", parseDecimal(ou.LastFilledPrice),
				"status", ou.Status,
				"exchange_ts", ts,
			)
			orderHandler(exchange.OrderEvent{
				Type:       evType,
				OrderID:    orderID,
				TradeID:    strconv.FormatInt(ou.TradeID, 10),
				Symbol:     ou.Symbol,
				Side:       side,
				Qty:        parseDecimal(ou.OriginalQty),
				FilledQty:  qty,
				FilledAvg:  parseDecimal(ou.LastFilledPrice),
				Commission: parseDecimal(ou.Commission),
				Timestamp:  ts,
			})
		case "CANCELED", "EXPIRED", "CALCULATED":
			orderHandler(exchange.OrderEvent{
				Type:      exchange.OrderEventCanceled,
				OrderID:   orderID,
				TradeID:   orderID + "_cancel",
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.OriginalQty),
				Timestamp: ts,
			})
		}
	}, func(err error) {
		slog.Warn("binance: futures ws error", "err", err)
	})
	futures.UseDemo = false
	wsMu.Unlock()
	if err != nil {
		return fmt.Errorf("ws futures user data: %w", err)
	}

	keepAlive := time.NewTicker(25 * time.Minute)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			close(stopC)
			return nil
		case <-keepAlive.C:
			if err := fut.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(ctx); err != nil {
				slog.Warn("binance: futures listen key keep-alive failed", "err", err)
			}
		case <-doneC:
			return fmt.Errorf("futures user stream closed")
		}
	}
}

// ── COINM Delivery ────────────────────────────────────────────────────────────

func (c *Client) streamDeliveryOrders(ctx context.Context, creds exchange.Credentials, orderHandler func(exchange.OrderEvent)) {
	bo := exchange.Backoff{Min: 5 * time.Second, Max: 5 * time.Minute, Factor: 2.0, Jitter: true}
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := c.streamDeliveryOrdersOnce(ctx, creds, orderHandler)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			attempt = 0
		}
		wait := bo.Next(attempt)
		attempt++
		slog.Warn("binance: delivery order stream disconnected, reconnecting", "err", err, "attempt", attempt, "retry_in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) streamDeliveryOrdersOnce(ctx context.Context, creds exchange.Credentials, orderHandler func(exchange.OrderEvent)) error {
	del := c.newDelivery(creds)
	listenKey, err := del.NewStartUserStreamService().Do(ctx)
	if err != nil {
		return fmt.Errorf("start delivery user stream: %w", err)
	}

	wsMu.Lock()
	delivery.UseDemo = c.paper
	doneC, stopC, err := delivery.WsUserDataServe(listenKey, func(event *delivery.WsUserDataEvent) {
		if event.Event != delivery.UserDataEventTypeOrderTradeUpdate {
			return
		}
		ou := event.OrderTradeUpdate
		side := exchange.Buy
		if ou.Side == delivery.SideTypeSell {
			side = exchange.Sell
		}
		ts := time.UnixMilli(ou.TradeTime).UTC()
		orderID := strconv.FormatInt(ou.ID, 10)

		switch ou.ExecutionType {
		case "NEW":
			orderHandler(exchange.OrderEvent{
				Type:      exchange.OrderEventLive,
				OrderID:   orderID,
				TradeID:   orderID + "_open",
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.OriginalQty),
				Timestamp: ts,
			})
		case "TRADE":
			evType := exchange.OrderEventPartialFill
			if ou.Status == delivery.OrderStatusTypeFilled {
				evType = exchange.OrderEventFilled
			}
			qty := parseDecimal(ou.LastFilledQty)
			if !qty.IsPositive() {
				return
			}
			slog.Info("binance: delivery fill received",
				"order_id", orderID,
				"trade_id", ou.TradeID,
				"symbol", ou.Symbol,
				"side", side,
				"fill_qty", qty,
				"fill_avg", parseDecimal(ou.LastFilledPrice),
				"status", ou.Status,
				"exchange_ts", ts,
			)
			orderHandler(exchange.OrderEvent{
				Type:       evType,
				OrderID:    orderID,
				TradeID:    strconv.FormatInt(ou.TradeID, 10),
				Symbol:     ou.Symbol,
				Side:       side,
				Qty:        parseDecimal(ou.OriginalQty),
				FilledQty:  qty,
				FilledAvg:  parseDecimal(ou.LastFilledPrice),
				Commission: parseDecimal(ou.Commission),
				Timestamp:  ts,
			})
		case "CANCELED", "EXPIRED", "CALCULATED":
			orderHandler(exchange.OrderEvent{
				Type:      exchange.OrderEventCanceled,
				OrderID:   orderID,
				TradeID:   orderID + "_cancel",
				Symbol:    ou.Symbol,
				Side:      side,
				Qty:       parseDecimal(ou.OriginalQty),
				Timestamp: ts,
			})
		}
	}, func(err error) {
		slog.Warn("binance: delivery ws error", "err", err)
	})
	delivery.UseDemo = false
	wsMu.Unlock()
	if err != nil {
		return fmt.Errorf("ws delivery user data: %w", err)
	}

	keepAlive := time.NewTicker(25 * time.Minute)
	defer keepAlive.Stop()

	for {
		select {
		case <-ctx.Done():
			close(stopC)
			return nil
		case <-keepAlive.C:
			if err := del.NewKeepaliveUserStreamService().ListenKey(listenKey).Do(ctx); err != nil {
				slog.Warn("binance: delivery listen key keep-alive failed", "err", err)
			}
		case <-doneC:
			return fmt.Errorf("delivery user stream closed")
		}
	}
}
