package action

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// bybitStablecoins are cash-equivalent assets excluded from spot position reporting.
var bybitStablecoins = map[string]bool{
	"USDT":  true,
	"USDC":  true,
	"BUSD":  true,
	"DAI":   true,
	"FDUSD": true,
}

// ListOpenOrders returns all live/partially-filled orders across spot and linear categories.
func (c *Client) ListOpenOrders(ctx context.Context, creds exchange.Credentials, symbol string) ([]exchange.OrderResult, error) {
	spot, err := c.GetOrders(ctx, creds, "spot", symbol, "")
	if err != nil {
		return nil, fmt.Errorf("bybit list open orders (spot): %w", err)
	}

	linear, linErr := c.GetOrders(ctx, creds, "linear", symbol, "")
	if linErr != nil {
		slog.Warn("bybit list open orders (linear): skipping", "err", linErr)
	}

	return append(spot, linear...), nil
}

// ListPositions returns all currently held positions in the Unified Trading Account.
//
// Spot: non-stablecoin coins with positive walletBalance from UNIFIED account.
// Linear (perpetuals): open positions from /v5/position/list?category=linear.
func (c *Client) ListPositions(ctx context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	info, err := c.GetWalletBalance(ctx, creds, "UNIFIED")
	if err != nil {
		return nil, fmt.Errorf("bybit list positions (wallet): %w", err)
	}

	var results []exchange.PositionResult

	// Spot holdings: non-stablecoin coins with walletBalance > 0
	for _, coin := range info.Coins {
		if bybitStablecoins[coin.Coin] {
			continue
		}
		qty := decimal.NewFromFloat(coin.WalletBalance)
		if !qty.IsPositive() {
			continue
		}
		results = append(results, exchange.PositionResult{
			Symbol: coin.Coin + "USDT",
			Side:   exchange.Buy,
			Qty:    qty,
		})
	}

	// Linear (USDT-M perpetuals/futures) positions
	var posResp apiResponse[positionListResult]
	body := map[string]string{"category": "linear", "settleCoin": "USDT"}
	if err := c.doSigned(ctx, creds, "GET", "/v5/position/list", body, &posResp); err != nil {
		slog.Warn("bybit list positions (linear): skipping", "err", err)
		return results, nil
	}
	if posResp.RetCode != 0 {
		slog.Warn("bybit list positions (linear): bad response", "code", posResp.RetCode, "msg", posResp.RetMsg)
		return results, nil
	}

	for _, p := range posResp.Result.List {
		size := parseDecimal(p.Size)
		if size.IsZero() {
			continue
		}
		side := exchange.Buy
		if p.Side == "Sell" {
			side = exchange.Sell
		}
		results = append(results, exchange.PositionResult{
			Symbol:    p.Symbol,
			Side:      side,
			Qty:       size,
			AvgPrice:  parseDecimal(p.AvgPrice),
			UnrealPnL: parseDecimal(p.UnrealisedPnl),
		})
	}

	return results, nil
}

// SubscribeFills opens a fill stream by bridging StreamOrders.
// The returned channel is closed when ctx is cancelled.
func (c *Client) SubscribeFills(ctx context.Context, creds exchange.Credentials) (<-chan exchange.FillEvent, error) {
	ch := make(chan exchange.FillEvent, 64)

	send := func(fill exchange.FillEvent) {
		defer func() { recover() }() //nolint:errcheck
		select {
		case ch <- fill:
		case <-ctx.Done():
		}
	}

	if err := c.StreamOrders(ctx, creds, func(ev exchange.OrderEvent) {
		if ev.Type != exchange.OrderEventFilled && ev.Type != exchange.OrderEventPartialFill {
			return
		}
		send(exchange.FillEvent{
			OrderID:   ev.OrderID,
			Symbol:    ev.Symbol,
			Side:      ev.Side,
			FilledQty: ev.FilledQty,
			FillPrice: ev.FilledAvg,
			Timestamp: ev.Timestamp,
		})
	}); err != nil {
		return nil, fmt.Errorf("bybit subscribe fills: %w", err)
	}

	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// positionListResult is the Bybit V5 /v5/position/list response envelope.
type positionListResult struct {
	List []positionDetail `json:"list"`
}

type positionDetail struct {
	Symbol        string `json:"symbol"`
	Side          string `json:"side"` // "Buy" | "Sell" | "None"
	Size          string `json:"size"` // absolute position size
	AvgPrice      string `json:"avgPrice"`
	UnrealisedPnl string `json:"unrealisedPnl"`
}
