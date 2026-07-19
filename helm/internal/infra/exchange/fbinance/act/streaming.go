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

	"github.com/adshao/go-binance/v2/futures"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// isPermanentStreamError returns true for errors that won't self-heal on retry.
func isPermanentStreamError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "-2015") ||
		strings.Contains(s, "-2014") ||
		strings.Contains(s, "410") ||
		strings.Contains(s, "401")
}

// wsMu guards futures.UseTestnet global flag.
var wsMu sync.Mutex

// StreamOrders implements exchange.AccountStreamer for USDM futures.
func (c *Client) StreamOrders(
	ctx context.Context,
	creds exchange.Credentials,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	onBalance func(exchange.BalanceEvent), // pushed on ACCOUNT_UPDATE's balances array
	onPosition func(exchange.PositionEvent),
	onRisk func(exchange.RiskEvent),
	onCredentialError func(string),
) error {
	go c.streamFuturesOrders(ctx, c.newFut(creds), onLifecycle, onFill, onBalance, onPosition, onRisk, onCredentialError)
	slog.Info("fbinance: futures order streaming started")
	return nil
}

func (c *Client) streamFuturesOrders(
	ctx context.Context,
	fut *futures.Client,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	onBalance func(exchange.BalanceEvent),
	onPosition func(exchange.PositionEvent),
	onRisk func(exchange.RiskEvent),
	onCredentialError func(string),
) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("goroutine panic recovered", "recover", r)
		}
	}()
	bo := exchange.Backoff{Min: 5 * time.Second, Max: 5 * time.Minute, Factor: 2.0, Jitter: true}
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := c.streamFuturesOrdersOnce(ctx, fut, onLifecycle, onFill, onBalance, onPosition, onRisk)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 30*time.Second {
			attempt = 0
		}
		if isPermanentStreamError(err) {
			slog.Error("fbinance: permanent stream error — stopping reconnect loop", "err", err)
			if onCredentialError != nil {
				onCredentialError(fmt.Errorf("fbinance WS stream: %w", err).Error())
			}
			return
		}
		wait := bo.Next(attempt)
		attempt++
		slog.Warn("fbinance: futures order stream disconnected, reconnecting", "err", err, "attempt", attempt, "retry_in", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

func (c *Client) streamFuturesOrdersOnce(
	ctx context.Context,
	fut *futures.Client,
	onLifecycle func(exchange.OrderLifecycleEvent),
	onFill func(exchange.WsFillEvent),
	onBalance func(exchange.BalanceEvent),
	onPosition func(exchange.PositionEvent),
	onRisk func(exchange.RiskEvent),
) error {
	listenKey, err := fut.NewStartUserStreamService().Do(ctx)
	if err != nil {
		return fmt.Errorf("start futures user stream: %w", err)
	}

	var wsErr error
	wsMu.Lock()
	futures.UseDemo = c.paper
	doneC, stopC, err := futures.WsUserDataServe(listenKey, func(event *futures.WsUserDataEvent) {
		if rawJSON, err := json.Marshal(event); err == nil {
			slog.Info("fbinance: raw futures ws event", "raw", string(rawJSON))
		} else {
			slog.Warn("fbinance: failed to marshal futures ws event", "err", err)
		}
		switch event.Event {
		case futures.UserDataEventTypeAccountUpdate:
			// ACCOUNT_UPDATE carries both balance and position changes.
			if onBalance != nil {
				for _, bal := range event.AccountUpdate.Balances {
					onBalance(exchange.BalanceEvent{
						Asset: bal.Asset,
						Free:  parseDecimal(bal.Balance),
						At:    time.Now().UTC(),
					})
				}
			}
			if onPosition != nil {
				for _, pos := range event.AccountUpdate.Positions {
					side := exchange.PositionNet
					switch pos.Side {
					case futures.PositionSideTypeLong:
						side = exchange.PositionLong
					case futures.PositionSideTypeShort:
						side = exchange.PositionShort
					}
					onPosition(exchange.PositionEvent{
						Symbol:        pos.Symbol,
						Side:          side,
						Size:          parseDecimal(pos.Amount),
						EntryPrice:    parseDecimal(pos.EntryPrice),
						UnrealizedPnL: parseDecimal(pos.UnrealizedPnL),
						At:            time.Now().UTC(),
					})
				}
			}
			return
		case futures.UserDataEventTypeMarginCall:
			// MARGIN_CALL — emit RiskEvent for each position in margin call.
			// MarginRatio = maintenance margin required / the wallet balance that
			// margin is drawn from (isolated wallet for an isolated position,
			// account cross-wallet balance otherwise) — Binance's own documented
			// risk relationship, not an estimate. LiquidationPrice isn't in this
			// payload at all (Binance doesn't push it on MARGIN_CALL); left at
			// zero rather than approximated from mark price/leverage.
			if onRisk != nil {
				for _, p := range event.MarginCallPositions {
					denom := event.CrossWalletBalance
					if p.MarginType == futures.MarginTypeIsolated {
						denom = p.IsolatedWallet
					}
					var ratio decimal.Decimal
					if d := parseDecimal(denom); d.IsPositive() {
						ratio = parseDecimal(p.MaintenanceMarginRequired).Div(d)
					}
					onRisk(exchange.RiskEvent{
						Symbol:      p.Symbol,
						MarginRatio: ratio,
						At:          time.Now().UTC(),
					})
				}
			}
			return
		case futures.UserDataEventTypeOrderTradeUpdate:
			// handled below
		default:
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
			if onLifecycle != nil {
				onLifecycle(exchange.OrderLifecycleEvent{
					Type:          exchange.OrderLifecycleEventLive,
					OrderID:       orderID,
					ClientOrderID: ou.ClientOrderID,
					Symbol:        ou.Symbol,
					Side:          side,
					Qty:           parseDecimal(ou.OriginalQty),
					Timestamp:     ts,
				})
			}
		case "TRADE":
			if onFill == nil {
				return
			}
			qty := parseDecimal(ou.LastFilledQty)
			if !qty.IsPositive() {
				return
			}
			slog.Info("fbinance: futures fill received",
				"order_id", orderID,
				"trade_id", ou.TradeID,
				"symbol", ou.Symbol,
				"side", side,
				"fill_qty", qty,
				"fill_avg", parseDecimal(ou.LastFilledPrice),
				"status", ou.Status,
				"exchange_ts", ts,
			)
			onFill(exchange.WsFillEvent{
				OrderID:       orderID,
				ClientOrderID: ou.ClientOrderID,
				TradeID:       strconv.FormatInt(ou.TradeID, 10),
				Symbol:        ou.Symbol,
				Side:          side,
				Partial:       ou.Status != futures.OrderStatusTypeFilled,
				FilledQty:     qty,
				FilledAvg:     parseDecimal(ou.LastFilledPrice),
				Commission:    parseDecimal(ou.Commission),
				Timestamp:     ts,
			})
		case "CANCELED", "EXPIRED", "CALCULATED":
			if onLifecycle != nil {
				onLifecycle(exchange.OrderLifecycleEvent{
					Type:          exchange.OrderLifecycleEventCanceled,
					OrderID:       orderID,
					ClientOrderID: ou.ClientOrderID,
					Symbol:        ou.Symbol,
					Side:          side,
					Qty:           parseDecimal(ou.OriginalQty),
					Timestamp:     ts,
				})
			}
		}
	}, func(err error) {
		slog.Warn("fbinance: futures ws error", "err", err)
		wsErr = err
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
				slog.Warn("fbinance: futures listen key keep-alive failed", "err", err)
			}
		case <-doneC:
			if wsErr != nil {
				return fmt.Errorf("futures user stream closed: %w", wsErr)
			}
			return fmt.Errorf("futures user stream closed")
		}
	}
}
