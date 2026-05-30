package act

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"

	"mallow/helm/internal/infra/exchange"
)

// PlaceOrder routes to spot or futures endpoint based on req.Market.
func (c *Client) PlaceOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	if req.Market == exchange.MarketFutures {
		return c.placeFuturesOrder(ctx, creds, req)
	}
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

func (c *Client) placeFuturesOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	side := futures.SideTypeBuy
	if req.Side == exchange.Sell {
		side = futures.SideTypeSell
	}
	orderType := futures.OrderTypeMarket
	if req.Type == exchange.Limit {
		orderType = futures.OrderTypeLimit
	}

	svc := c.newFut(creds).NewCreateOrderService().
		Symbol(req.Symbol).
		Side(side).
		Type(orderType).
		Quantity(req.Qty.String()).
		ReduceOnly(req.ReduceOnly)
	if req.ClientOrderID != "" {
		svc = svc.NewClientOrderID(req.ClientOrderID)
	}
	if orderType == futures.OrderTypeLimit {
		svc = svc.TimeInForce(binanceFuturesTIF(req.TIF)).Price(req.Price.String())
	}

	slog.Info("binance: placing futures order", "symbol", req.Symbol, "side", req.Side,
		"qty", req.Qty, "type", orderType, "reduce_only", req.ReduceOnly)
	t0 := time.Now()
	resp, err := svc.Do(ctx)
	if err != nil {
		slog.Error("binance: futures order rejected", "symbol", req.Symbol, "err", err,
			"rest_latency", time.Since(t0).Truncate(time.Millisecond))
		return nil, fmt.Errorf("binance futures place order: %w", err)
	}
	result := futuresCreateToResult(req.Side, resp)
	slog.Info("binance: futures order ack", "symbol", result.Symbol, "order_id", result.ID,
		"status", result.Status, "filled_qty", result.FilledQty,
		"rest_latency", time.Since(t0).Truncate(time.Millisecond))
	return result, nil
}

// parseSpotOrderID splits the "SYMBOL:numericID" format returned by placeSpotOrder.
// Futures order IDs are plain numeric strings — use GetFuturesOrder for those.
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
// For futures orders, use GetFuturesOrder.
func (c *Client) GetOrder(ctx context.Context, creds exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	symbol, oid, err := parseSpotOrderID(orderID)
	if err != nil {
		return nil, err
	}
	resp, err := c.newSpot(creds).NewGetOrderService().Symbol(symbol).OrderID(oid).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get order: %w", err)
	}
	return spotGetToResult(orderID, resp), nil
}

// GetOrderByClientOrderID looks up an order by the caller-supplied clOrdId on the
// endpoint matching market (futures → /fapi/, otherwise spot). Returns nil when no such
// order exists or the lookup fails — the caller treats nil as "could not confirm".
// See CLIENT_ORDER_ID.md.
func (c *Client) GetOrderByClientOrderID(ctx context.Context, creds exchange.Credentials, symbol string, market exchange.MarketKind, clid string) (*exchange.OrderResult, error) {
	if market == exchange.MarketFutures {
		if c.paper {
			return nil, nil // futures lookups unsupported on paper accounts
		}
		fresp, ferr := c.newFut(creds).NewGetOrderService().
			Symbol(symbol).OrigClientOrderID(clid).Do(ctx)
		if ferr != nil || fresp == nil {
			return nil, nil
		}
		return &exchange.OrderResult{
			ID:            strconv.FormatInt(fresp.OrderID, 10),
			ClientOrderID: fresp.ClientOrderID,
			Symbol:        fresp.Symbol,
			Side:          exchange.OrderSide(strings.ToLower(string(fresp.Side))),
			Status:        strings.ToLower(string(fresp.Status)),
			Qty:           parseDecimal(fresp.OrigQuantity),
			FilledQty:     parseDecimal(fresp.ExecutedQuantity),
		}, nil
	}
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
	return err
}

// GetFuturesOrder retrieves a futures order by symbol + numeric order ID.
func (c *Client) GetFuturesOrder(ctx context.Context, creds exchange.Credentials, symbol string, orderID int64) (*exchange.OrderResult, error) {
	resp, err := c.newFut(creds).NewGetOrderService().Symbol(symbol).OrderID(orderID).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance futures get order: %w", err)
	}
	return &exchange.OrderResult{
		ID:        strconv.FormatInt(resp.OrderID, 10),
		Symbol:    resp.Symbol,
		Side:      exchange.OrderSide(string(resp.Side)),
		Status:    strings.ToLower(string(resp.Status)),
		Qty:       parseDecimal(resp.OrigQuantity),
		FilledQty: parseDecimal(resp.ExecutedQuantity),
	}, nil
}

// CancelFuturesOrder cancels a futures order by symbol + numeric order ID.
func (c *Client) CancelFuturesOrder(ctx context.Context, creds exchange.Credentials, symbol string, orderID int64) error {
	_, err := c.newFut(creds).NewCancelOrderService().Symbol(symbol).OrderID(orderID).Do(ctx)
	if err != nil {
		return fmt.Errorf("binance futures cancel order: %w", err)
	}
	return nil
}

// ── Exit / bracket orders ─────────────────────────────────────────────────────

// PlaceExitOrders places SL/TP bracket orders after an entry fill.
// Spot: OCO when both are set; individual STOP_LOSS_LIMIT / TAKE_PROFIT_LIMIT otherwise.
// Futures: separate STOP_MARKET + TAKE_PROFIT_MARKET algo orders (reduce-only).
func (c *Client) PlaceExitOrders(ctx context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	if req.Market == exchange.MarketFutures {
		return c.placeFuturesExitOrders(ctx, creds, req)
	}
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

func (c *Client) placeFuturesExitOrders(ctx context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	side := futures.SideTypeSell
	if req.Side == exchange.Buy {
		side = futures.SideTypeBuy
	}

	var ids []string

	if req.StopLoss.IsPositive() {
		slog.Info("binance: placing futures SL exit", "symbol", req.Symbol, "side", side, "sl", req.StopLoss)
		resp, err := c.newFut(creds).NewCreateAlgoOrderService().
			Symbol(req.Symbol).
			Side(side).
			Type(futures.AlgoOrderTypeStopMarket).
			TriggerPrice(req.StopLoss.String()).
			Quantity(req.Qty.String()).
			ReduceOnly(true).
			Do(ctx)
		if err != nil {
			return nil, fmt.Errorf("binance futures SL exit: %w", err)
		}
		ids = append(ids, strconv.FormatInt(resp.AlgoId, 10))
	}

	if req.TakeProfit.IsPositive() {
		slog.Info("binance: placing futures TP exit", "symbol", req.Symbol, "side", side, "tp", req.TakeProfit)
		resp, err := c.newFut(creds).NewCreateAlgoOrderService().
			Symbol(req.Symbol).
			Side(side).
			Type(futures.AlgoOrderTypeTakeProfitMarket).
			TriggerPrice(req.TakeProfit.String()).
			Quantity(req.Qty.String()).
			ReduceOnly(true).
			Do(ctx)
		if err != nil {
			return nil, fmt.Errorf("binance futures TP exit: %w", err)
		}
		ids = append(ids, strconv.FormatInt(resp.AlgoId, 10))
	}

	return &exchange.ExitOrderResult{OrderIDs: ids}, nil
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

// binanceFuturesTIF maps the canonical TIF to Binance futures TimeInForce. Default: GTC.
func binanceFuturesTIF(tif exchange.TimeInForce) futures.TimeInForceType {
	switch tif {
	case exchange.TIFIOC:
		return futures.TimeInForceTypeIOC
	case exchange.TIFFOK:
		return futures.TimeInForceTypeFOK
	default:
		return futures.TimeInForceTypeGTC
	}
}
