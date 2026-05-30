package act

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"mallow/helm/internal/infra/exchange"
)

// PlaceOrder routes to spot or linear (perpetual) category based on req.Market.
func (c *Client) PlaceOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	category := "spot"
	if req.Market == exchange.MarketFutures {
		category = "linear"
	}

	orderType := "Market"
	if req.Type == exchange.Limit {
		orderType = "Limit"
	}

	body := createOrderReq{
		Category:    category,
		Symbol:      req.Symbol,
		Side:        toBybitSide(req.Side),
		OrderType:   orderType,
		Qty:         req.Qty.String(),
		OrderLinkId: req.ClientOrderID,
		TimeInForce: bybitTIF(req.TIF),
		ReduceOnly:  req.ReduceOnly,
	}
	// Bybit V5 spot market Buy: qty defaults to quote coin (USDT); specify baseCoin so qty is in BTC.
	if category == "spot" && orderType == "Market" && req.Side == exchange.Buy {
		body.MarketUnit = "baseCoin"
	}
	if orderType == "Limit" && req.Price.IsPositive() {
		body.Price = req.Price.String()
	}

	slog.Info("bybit: placing order", "symbol", req.Symbol, "side", req.Side, "qty", req.Qty, "category", category)

	var resp apiResponse[createOrderResult]
	if err := c.doSigned(ctx, creds, "POST", "/v5/order/create", body, &resp); err != nil {
		return nil, fmt.Errorf("place order: %w", err)
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit order failed: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}

	return &exchange.OrderResult{
		ID:            resp.Result.OrderID,
		ClientOrderID: resp.Result.OrderLinkId,
		Symbol:        req.Symbol,
		Side:          req.Side,
		Status:        "submitted",
		Qty:           req.Qty,
	}, nil
}

// GetOrder retrieves order status by ID (searches spot category by default).
func (c *Client) GetOrder(ctx context.Context, creds exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	body := map[string]string{"category": "spot", "orderId": orderID}

	var resp apiResponse[orderListResult]
	if err := c.doSigned(ctx, creds, "GET", "/v5/order/realtime", body, &resp); err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	if resp.RetCode != 0 || len(resp.Result.List) == 0 {
		return nil, fmt.Errorf("bybit get order: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	return orderDetailToResult(&resp.Result.List[0]), nil
}

// GetOrderByClientOrderID looks up an order by orderLinkId (the Bybit clOrdId) on the
// category matching market (futures → linear, otherwise spot). Returns nil when no such
// order exists or the lookup fails. See CLIENT_ORDER_ID.md.
func (c *Client) GetOrderByClientOrderID(ctx context.Context, creds exchange.Credentials, _ string, market exchange.MarketKind, clid string) (*exchange.OrderResult, error) {
	category := "spot"
	if market == exchange.MarketFutures {
		category = "linear"
	}
	body := map[string]string{"category": category, "orderLinkId": clid}
	var resp apiResponse[orderListResult]
	if err := c.doSigned(ctx, creds, "GET", "/v5/order/realtime", body, &resp); err != nil {
		return nil, nil
	}
	if resp.RetCode != 0 || len(resp.Result.List) == 0 {
		return nil, nil
	}
	return orderDetailToResult(&resp.Result.List[0]), nil
}

// GetOrders lists orders with optional category/symbol/status/orderFilter filters.
// orderFilter: "" (regular orders), "StopOrder" (conditional stop/TP orders), "tpslOrder".
func (c *Client) GetOrders(ctx context.Context, creds exchange.Credentials, category, symbol, status string, orderFilter ...string) ([]exchange.OrderResult, error) {
	body := map[string]string{"category": category}
	if symbol != "" {
		body["symbol"] = symbol
	}
	if status != "" {
		body["orderStatus"] = status
	}
	if len(orderFilter) > 0 && orderFilter[0] != "" {
		body["orderFilter"] = orderFilter[0]
	}

	var resp apiResponse[orderListResult]
	if err := c.doSigned(ctx, creds, "GET", "/v5/order/realtime", body, &resp); err != nil {
		return nil, fmt.Errorf("get orders: %w", err)
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit get orders: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}

	results := make([]exchange.OrderResult, len(resp.Result.List))
	for i := range resp.Result.List {
		results[i] = *orderDetailToResult(&resp.Result.List[i])
	}
	return results, nil
}

// CancelOrder cancels a pending order by ID.
func (c *Client) CancelOrder(ctx context.Context, creds exchange.Credentials, orderID string) error {
	body := cancelOrderReq{Category: "spot", OrderID: orderID}
	var resp apiResponse[json.RawMessage]
	if err := c.doSigned(ctx, creds, "POST", "/v5/order/cancel", body, &resp); err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit cancel: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	return nil
}

// AmendOrder modifies an existing order's qty or price.
func (c *Client) AmendOrder(ctx context.Context, creds exchange.Credentials, category, symbol, orderID, newQty, newPrice string) error {
	body := map[string]string{"category": category, "symbol": symbol, "orderId": orderID}
	if newQty != "" {
		body["qty"] = newQty
	}
	if newPrice != "" {
		body["price"] = newPrice
	}
	var resp apiResponse[json.RawMessage]
	if err := c.doSigned(ctx, creds, "POST", "/v5/order/amend", body, &resp); err != nil {
		return fmt.Errorf("amend order: %w", err)
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit amend: code=%d msg=%s", resp.RetCode, resp.RetMsg)
	}
	return nil
}

// ── Exit / bracket orders ─────────────────────────────────────────────────────

// PlaceExitOrders places exchange-side SL/TP conditional orders after an entry fill.
// Spot: stop-market trigger orders via stopOrderType=Stop/TakeProfit.
// Linear: same but with reduceOnly=true.
func (c *Client) PlaceExitOrders(ctx context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	category := "spot"
	if req.Market == exchange.MarketFutures {
		category = "linear"
	}
	side := toBybitSide(req.Side)
	reduceOnly := req.Market == exchange.MarketFutures

	var ids []string

	if req.StopLoss.IsPositive() {
		body := createOrderReq{
			Category:      category,
			Symbol:        req.Symbol,
			Side:          side,
			OrderType:     "Market",
			Qty:           req.Qty.String(),
			TimeInForce:   "IOC",
			ReduceOnly:    reduceOnly,
			StopOrderType: "Stop",
			TriggerPrice:  req.StopLoss.String(),
			TriggerBy:     "LastPrice",
		}
		slog.Info("bybit: placing SL exit", "symbol", req.Symbol, "side", side, "sl", req.StopLoss, "category", category)
		var resp apiResponse[createOrderResult]
		if err := c.doSigned(ctx, creds, "POST", "/v5/order/create", body, &resp); err != nil {
			return nil, fmt.Errorf("bybit SL exit: %w", err)
		}
		if resp.RetCode != 0 {
			return nil, fmt.Errorf("bybit SL exit: code=%d msg=%s", resp.RetCode, resp.RetMsg)
		}
		ids = append(ids, resp.Result.OrderID)
	}

	if req.TakeProfit.IsPositive() {
		body := createOrderReq{
			Category:      category,
			Symbol:        req.Symbol,
			Side:          side,
			OrderType:     "Market",
			Qty:           req.Qty.String(),
			TimeInForce:   "IOC",
			ReduceOnly:    reduceOnly,
			StopOrderType: "TakeProfit",
			TriggerPrice:  req.TakeProfit.String(),
			TriggerBy:     "LastPrice",
		}
		slog.Info("bybit: placing TP exit", "symbol", req.Symbol, "side", side, "tp", req.TakeProfit, "category", category)
		var resp apiResponse[createOrderResult]
		if err := c.doSigned(ctx, creds, "POST", "/v5/order/create", body, &resp); err != nil {
			return nil, fmt.Errorf("bybit TP exit: %w", err)
		}
		if resp.RetCode != 0 {
			return nil, fmt.Errorf("bybit TP exit: code=%d msg=%s", resp.RetCode, resp.RetMsg)
		}
		ids = append(ids, resp.Result.OrderID)
	}

	return &exchange.ExitOrderResult{OrderIDs: ids}, nil
}

// bybitTIF maps canonical TIF to Bybit V5 timeInForce string. Default: GTC.
func bybitTIF(tif exchange.TimeInForce) string {
	switch tif {
	case exchange.TIFIOC:
		return "IOC"
	case exchange.TIFFOK:
		return "FOK"
	case exchange.TIFDay:
		return "PostOnly" // closest Bybit analogue for session-scoped
	default:
		return "GTC"
	}
}
