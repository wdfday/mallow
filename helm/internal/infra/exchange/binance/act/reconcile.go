package act

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// ListOpenOrders returns all outstanding spot + futures orders.
// When symbol is empty, all open orders are fetched (higher weight on Binance).
func (c *Client) ListOpenOrders(ctx context.Context, creds exchange.Credentials, symbol string) ([]exchange.OrderResult, error) {
	// Spot
	spotSvc := c.newSpot(creds).NewListOpenOrdersService()
	if symbol != "" {
		spotSvc = spotSvc.Symbol(symbol)
	}
	spotOrders, err := spotSvc.Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance list open orders (spot): %w", err)
	}

	results := make([]exchange.OrderResult, 0, len(spotOrders))
	for _, o := range spotOrders {
		filledQty := parseDecimal(o.ExecutedQuantity)
		quoteQty := parseDecimal(o.CummulativeQuoteQuantity)
		var filledAvg decimal.Decimal
		if filledQty.IsPositive() {
			filledAvg = quoteQty.Div(filledQty)
		}
		results = append(results, exchange.OrderResult{
			ID:        o.Symbol + ":" + strconv.FormatInt(o.OrderID, 10),
			Symbol:    o.Symbol,
			Side:      binanceSide(o.Side),
			Status:    string(o.Status),
			Qty:       parseDecimal(o.OrigQuantity),
			FilledQty: filledQty,
			FilledAvg: filledAvg,
		})
	}

	// Futures (skipped on paper accounts)
	if !c.paper {
		futSvc := c.newFut(creds).NewListOpenOrdersService()
		if symbol != "" {
			futSvc = futSvc.Symbol(symbol)
		}
		futOrders, futErr := futSvc.Do(ctx)
		if futErr != nil {
			slog.Warn("binance list open orders (futures): skipping", "err", futErr)
		} else {
			for _, o := range futOrders {
				results = append(results, exchange.OrderResult{
					ID:        strconv.FormatInt(o.OrderID, 10),
					Symbol:    o.Symbol,
					Side:      exchange.OrderSide(string(o.Side)),
					Status:    string(o.Status),
					Qty:       parseDecimal(o.OrigQuantity),
					FilledQty: parseDecimal(o.ExecutedQuantity),
				})
			}
		}
	}

	return results, nil
}

// ListPositions returns all currently held positions.
// Spot: derives from non-stablecoin account balances.
// Futures: from /fapi/v2/positionRisk (skipped on paper accounts).
func (c *Client) ListPositions(ctx context.Context, creds exchange.Credentials) ([]exchange.PositionResult, error) {
	acct, err := c.newSpot(creds).NewGetAccountService().Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance list positions (spot): %w", err)
	}

	var results []exchange.PositionResult
	for _, b := range acct.Balances {
		free := parseDecimal(b.Free)
		locked := parseDecimal(b.Locked)
		total := free.Add(locked)
		if !total.IsPositive() {
			continue
		}
		switch b.Asset {
		case "USDT", "BUSD", "USDC", "FDUSD", "TUSD", "DAI":
			continue // stablecoins are cash, not positions
		}
		results = append(results, exchange.PositionResult{
			Symbol: b.Asset + "USDT",
			Side:   exchange.Buy,
			Qty:    total,
		})
	}

	if !c.paper {
		risk, futErr := c.newFut(creds).NewGetPositionRiskService().Do(ctx)
		if futErr != nil {
			slog.Warn("binance list positions (futures): skipping", "err", futErr)
		} else {
			for _, p := range risk {
				amt := parseDecimal(p.PositionAmt)
				if amt.IsZero() {
					continue
				}
				side := exchange.Buy
				if amt.IsNegative() {
					side = exchange.Sell
					amt = amt.Neg()
				}
				results = append(results, exchange.PositionResult{
					Symbol:    p.Symbol,
					Side:      side,
					Qty:       amt,
					AvgPrice:  parseDecimal(p.EntryPrice),
					UnrealPnL: parseDecimal(p.UnRealizedProfit),
				})
			}
		}
	}

	return results, nil
}

// SubscribeFills opens a fill stream by bridging StreamOrders.
// The returned channel is closed when ctx is cancelled.
func (c *Client) SubscribeFills(ctx context.Context, creds exchange.Credentials) (<-chan exchange.FillEvent, error) {
	ch := make(chan exchange.FillEvent, 64)

	send := func(fill exchange.FillEvent) {
		defer func() { recover() }() //nolint:errcheck // guard against send on closed ch
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
	}, nil); err != nil {
		return nil, fmt.Errorf("binance subscribe fills: %w", err)
	}

	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}
