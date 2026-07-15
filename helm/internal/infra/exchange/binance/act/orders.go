package act

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	gobinance "github.com/adshao/go-binance/v2"

	"mallow/helm/internal/infra/exchange"
)

// PlaceOrder places a spot order.
func (c *Client) PlaceOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	return c.placeSpotOrder(ctx, creds, req)
}

func (c *Client) placeSpotOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	side := gobinance.SideTypeBuy
	if req.Side == exchange.Sell {
		side = gobinance.SideTypeSell
	}
	orderType := gobinance.OrderTypeMarket
	if req.Type == exchange.Limit {
		orderType = gobinance.OrderTypeLimit
	}

	svc := c.newSpot(creds).NewCreateOrderService().
		Symbol(req.Symbol).
		Side(side).
		Type(orderType)
	if req.ClientOrderID != "" {
		svc = svc.NewClientOrderID(req.ClientOrderID)
	}
	if req.QuoteQty.IsPositive() {
		svc = svc.QuoteOrderQty(req.QuoteQty.String())
	} else {
		svc = svc.Quantity(req.Qty.String())
	}
	if orderType == gobinance.OrderTypeLimit {
		svc = svc.TimeInForce(binanceTIF(req.TIF)).Price(req.Price.String())
	}

	if req.QuoteQty.IsPositive() {
		slog.Info("binance: placing spot order", "symbol", req.Symbol, "side", req.Side,
			"quote_qty", req.QuoteQty, "type", orderType)
	} else {
		slog.Info("binance: placing spot order", "symbol", req.Symbol, "side", req.Side,
			"qty", req.Qty, "type", orderType)
	}
	t0 := time.Now()
	resp, err := svc.Do(ctx)
	if err != nil {
		slog.Error("binance: spot order rejected", "symbol", req.Symbol, "err", err,
			"rest_latency", time.Since(t0).Truncate(time.Millisecond))
		return nil, fmt.Errorf("binance spot place order: %w", err)
	}
	result := spotCreateToResult(req.Side, resp)
	slog.Info("binance: spot order ack", "symbol", result.Symbol, "order_id", result.ID,
		"status", result.Status, "filled_qty", result.FilledQty, "filled_avg", result.FilledAvg,
		"rest_latency", time.Since(t0).Truncate(time.Millisecond))
	return result, nil
}

// parseSpotOrderID splits the "SYMBOL:numericID" format returned by placeSpotOrder.
func parseSpotOrderID(orderID string) (symbol string, oid int64, err error) {
	parts := strings.SplitN(orderID, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("binance: invalid spot order ID %q (expected SYMBOL:id)", orderID)
	}
	oid, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("binance: malformed numeric ID in %q: %w", orderID, err)
	}
	return parts[0], oid, nil
}

// GetOrder retrieves a spot order by "SYMBOL:numericID" encoded order ID.
func (c *Client) GetOrder(ctx context.Context, creds exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	symbol, oid, err := parseSpotOrderID(orderID)
	if err != nil {
		return nil, err
	}
	resp, err := c.newSpot(creds).NewGetOrderService().Symbol(symbol).OrderID(oid).Do(ctx)
	if err != nil {
		// -2013: order does not exist — definitively gone from the exchange.
		// Return not_found so the poller can treat the associated leg as externally closed
		// rather than retrying indefinitely on a transient error.
		if isBinanceCode(err, -2013) {
			return &exchange.OrderResult{ID: orderID, Status: "not_found"}, nil
		}
		return nil, fmt.Errorf("binance get order: %w", err)
	}
	return spotGetToResult(orderID, resp), nil
}

// GetOrderByClientOrderID looks up a spot order by the caller-supplied clOrdId.
// Returns nil when no such order exists or the lookup fails — the caller treats nil as "could not confirm".
// See CLIENT_ORDER_ID.md.
func (c *Client) GetOrderByClientOrderID(ctx context.Context, creds exchange.Credentials, symbol string, market exchange.MarketKind, clid string) (*exchange.OrderResult, error) {
	resp, err := c.newSpot(creds).NewGetOrderService().
		Symbol(symbol).OrigClientOrderID(clid).Do(ctx)
	if err != nil || resp == nil {
		return nil, nil
	}
	return spotGetToResult(symbol+":"+strconv.FormatInt(resp.OrderID, 10), resp), nil
}

// CancelOrder cancels a spot order by "SYMBOL:numericID" encoded order ID.
func (c *Client) CancelOrder(ctx context.Context, creds exchange.Credentials, orderID string) error {
	symbol, oid, err := parseSpotOrderID(orderID)
	if err != nil {
		return err
	}
	_, err = c.newSpot(creds).NewCancelOrderService().Symbol(symbol).OrderID(oid).Do(ctx)
	if err != nil && isBinanceCode(err, -2011) {
		// -2011 "Unknown order sent": the order is already gone at the exchange —
		// same "definitively not there" signal -2013 gives GetOrder (see above),
		// just the cancel endpoint's version of it. The most common trigger is
		// cancelling one leg of a spot OCO right after its sibling leg already
		// auto-cancelled it. Treat as a successful no-op instead of a real
		// failure — every caller here wants "make sure nothing is left resting
		// under this ID," which is already true. Confirmed in production: every
		// 2-leg bracket cancel hit this on the second leg, logging a spurious
		// WARN on the majority-case exit path (see hand_runner.go's pre-exit
		// bracket cancel loop).
		return nil
	}
	return err
}

// PlaceExitOrders places SL/TP bracket orders after a spot entry fill.
// Spot: OCO when both are set; individual STOP_LOSS_LIMIT / TAKE_PROFIT_LIMIT otherwise.
func (c *Client) PlaceExitOrders(ctx context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	return c.placeSpotExitOrders(ctx, creds, req)
}

func (c *Client) placeSpotExitOrders(ctx context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	side := gobinance.SideTypeSell
	if req.Side == exchange.Buy {
		side = gobinance.SideTypeBuy
	}

	hasSL := req.StopLoss.IsPositive()
	hasTP := req.TakeProfit.IsPositive()

	if hasSL && hasTP {
		stopLimit := slippagePrice(req.StopLoss, req.Side)
		slog.Info("binance: placing spot OCO exit", "symbol", req.Symbol, "side", side,
			"tp", req.TakeProfit, "sl", req.StopLoss, "sl_limit", stopLimit)
		resp, err := c.newSpot(creds).NewCreateOCOService().
			Symbol(req.Symbol).
			Side(side).
			Quantity(req.Qty.String()).
			Price(req.TakeProfit.String()).
			StopPrice(req.StopLoss.String()).
			StopLimitPrice(stopLimit.String()).
			StopLimitTimeInForce(gobinance.TimeInForceTypeGTC).
			Do(ctx)
		if err != nil {
			return nil, fmt.Errorf("binance spot OCO exit: %w", err)
		}
		ids := make([]string, len(resp.Orders))
		for i, o := range resp.Orders {
			ids[i] = o.Symbol + ":" + strconv.FormatInt(o.OrderID, 10)
		}
		return &exchange.ExitOrderResult{OrderIDs: ids}, nil
	}

	if hasSL {
		stopLimit := slippagePrice(req.StopLoss, req.Side)
		slog.Info("binance: placing spot SL exit", "symbol", req.Symbol, "side", side,
			"sl", req.StopLoss, "sl_limit", stopLimit)
		resp, err := c.newSpot(creds).NewCreateOrderService().
			Symbol(req.Symbol).
			Side(side).
			Type(gobinance.OrderTypeStopLossLimit).
			Quantity(req.Qty.String()).
			StopPrice(req.StopLoss.String()).
			Price(stopLimit.String()).
			TimeInForce(gobinance.TimeInForceTypeGTC).
			Do(ctx)
		if err != nil {
			return nil, fmt.Errorf("binance spot SL exit: %w", err)
		}
		return &exchange.ExitOrderResult{
			OrderIDs: []string{req.Symbol + ":" + strconv.FormatInt(resp.OrderID, 10)},
		}, nil
	}

	// TP only — TAKE_PROFIT_LIMIT: trigger at stopPrice, place limit at same price.
	slog.Info("binance: placing spot TP exit", "symbol", req.Symbol, "side", side, "tp", req.TakeProfit)
	resp, err := c.newSpot(creds).NewCreateOrderService().
		Symbol(req.Symbol).
		Side(side).
		Type(gobinance.OrderTypeTakeProfitLimit).
		Quantity(req.Qty.String()).
		StopPrice(req.TakeProfit.String()).
		Price(req.TakeProfit.String()).
		TimeInForce(gobinance.TimeInForceTypeGTC).
		Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance spot TP exit: %w", err)
	}
	return &exchange.ExitOrderResult{
		OrderIDs: []string{req.Symbol + ":" + strconv.FormatInt(resp.OrderID, 10)},
	}, nil
}

// binanceTIF maps the canonical TIF to Binance spot TimeInForce. Default: GTC.
func binanceTIF(tif exchange.TimeInForce) gobinance.TimeInForceType {
	switch tif {
	case exchange.TIFIOC:
		return gobinance.TimeInForceTypeIOC
	case exchange.TIFFOK:
		return gobinance.TimeInForceTypeFOK
	default:
		return gobinance.TimeInForceTypeGTC
	}
}
