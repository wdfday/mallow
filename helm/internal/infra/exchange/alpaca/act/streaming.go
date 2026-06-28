package act

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// StreamOrders implements exchange.AccountStreamer.
// onBalance is ignored — Alpaca does not push balance updates on the trade-updates stream.
// onPosition and onRisk are ignored — Alpaca is spot-only; no futures positions or margin calls.
func (c *Client) StreamOrders(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	_ func(exchange.BalanceEvent),
	_ func(exchange.PositionEvent), // spot-only: no futures positions
	_ func(exchange.RiskEvent), // spot-only: no margin/liquidation
	onCredentialError func(string),
) error {
	slog.Info("alpaca: starting order stream")
	go c.streamOrdersLoop(ctx, creds, onLifecycle, onFill, onCredentialError)
	return nil
}

// isAlpacaAuthError detects permanent credential failures from the Alpaca WS stream.
func isAlpacaAuthError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "401") || strings.Contains(s, "unauthorized")
}

func (c *Client) streamOrdersLoop(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	onCredentialError func(string),
) {
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
		// lastCumFilled tracks cumulative FilledQty per order to compute the
		// incremental delta on each WS update. Alpaca sends cumulative totals;
		// WsFillEvent.FilledQty must always be incremental.
		// Reset on each reconnect so stale deltas from the previous connection
		// are never carried forward.
		lastCumFilled := make(map[string]decimal.Decimal)
		err := c.newSDK(creds).StreamTradeUpdates(ctx, func(tu alpacasdk.TradeUpdate) {
			handleTradeUpdate(tu, lastCumFilled, onLifecycle, onFill)
		}, alpacasdk.StreamTradeUpdatesRequest{})
		if ctx.Err() != nil {
			return
		}
		if isAlpacaAuthError(err) {
			slog.Error("alpaca: order stream auth failure — stopping reconnect loop", "err", err)
			if onCredentialError != nil {
				onCredentialError(fmt.Errorf("alpaca WS stream: %w", err).Error())
			}
			return
		}
		if time.Since(start) > 30*time.Second {
			attempt = 0
		}
		wait := bo.Next(attempt)
		attempt++
		slog.Warn("alpaca: order stream disconnected, reconnecting", "err", err, "attempt", attempt, "retry_in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

// handleTradeUpdate converts an Alpaca TradeUpdate to the appropriate typed event.
//
// Alpaca sends CUMULATIVE FilledQty (tu.Order.FilledQty) for both partial_fill and
// fill events — this violates the WsFillEvent.FilledQty incremental contract.
// We track the cumulative total per order in lastCumFilled and emit only the delta.
// lastCumFilled is keyed by orderID and reset on each reconnect.
func handleTradeUpdate(
	tu alpacasdk.TradeUpdate,
	lastCumFilled map[string]decimal.Decimal,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
) {
	side := exchange.Buy
	if string(tu.Order.Side) == "sell" {
		side = exchange.Sell
	}
	ts := time.Now().UTC()
	var origQty decimal.Decimal
	if tu.Order.Qty != nil {
		origQty = *tu.Order.Qty
	}

	switch tu.Event {
	case "new", "pending_new", "accepted":
		if onLifecycle != nil {
			onLifecycle(exchange.OrderLifecycleEvent{
				Type:          exchange.OrderLifecycleEventLive,
				OrderID:       tu.Order.ID,
				ClientOrderID: tu.Order.ClientOrderID,
				Symbol:        tu.Order.Symbol,
				Side:          side,
				Qty:           origQty,
				Timestamp:     ts,
			})
		}
	case "fill", "partial_fill":
		if onFill == nil {
			return
		}
		var avg decimal.Decimal
		if tu.Order.FilledAvgPrice != nil {
			avg = *tu.Order.FilledAvgPrice
		}
		// tu.Order.FilledQty is CUMULATIVE — compute the incremental delta.
		cumQty := tu.Order.FilledQty
		prev := lastCumFilled[tu.Order.ID]
		delta := cumQty.Sub(prev)
		if !delta.IsPositive() {
			// Already applied (reconnect replay or duplicate) — skip.
			return
		}
		lastCumFilled[tu.Order.ID] = cumQty
		if tu.Event == "fill" {
			// Terminal — cleanup tracking for this order.
			delete(lastCumFilled, tu.Order.ID)
		}
		tradeID := tu.ExecutionID
		if tradeID == "" {
			tradeID = tu.Order.ID + "_fill"
		}
		onFill(exchange.WsFillEvent{
			OrderID:       tu.Order.ID,
			ClientOrderID: tu.Order.ClientOrderID,
			TradeID:       tradeID,
			Symbol:        tu.Order.Symbol,
			Side:          side,
			Partial:       tu.Event == "partial_fill",
			FilledQty:     delta, // INCREMENTAL
			FilledAvg:     avg,
			Timestamp:     ts,
		})
	case "canceled", "expired", "replaced", "rejected":
		delete(lastCumFilled, tu.Order.ID) // cleanup
		if onLifecycle != nil {
			onLifecycle(exchange.OrderLifecycleEvent{
				Type:          exchange.OrderLifecycleEventCanceled,
				OrderID:       tu.Order.ID,
				ClientOrderID: tu.Order.ClientOrderID,
				Symbol:        tu.Order.Symbol,
				Side:          side,
				Qty:           origQty,
				Timestamp:     ts,
			})
		}
	}
}
