package action

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"orchestrator/internal/infra/exchange"
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

	body := createOrderRequest{
		Category:    category,
		Symbol:      req.Symbol,
		Side:        mapSide(req.Side),
		OrderType:   orderType,
		Qty:         req.Qty.String(),
		TimeInForce: "GTC",
		ReduceOnly:  req.ReduceOnly,
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
		ID:     resp.Result.OrderID,
		Symbol: req.Symbol,
		Side:   req.Side,
		Status: "submitted",
		Qty:    req.Qty,
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
	return mapOrderDetail(&resp.Result.List[0]), nil
}

// GetOrders lists orders with optional status filter.
func (c *Client) GetOrders(ctx context.Context, creds exchange.Credentials, category, symbol, status string) ([]exchange.OrderResult, error) {
	body := map[string]string{"category": category}
	if symbol != "" {
		body["symbol"] = symbol
	}
	if status != "" {
		body["orderStatus"] = status
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
		results[i] = *mapOrderDetail(&resp.Result.List[i])
	}
	return results, nil
}

// CancelOrder cancels a pending order by ID.
func (c *Client) CancelOrder(ctx context.Context, creds exchange.Credentials, orderID string) error {
	body := cancelOrderRequest{Category: "spot", OrderID: orderID}
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

func mapOrderDetail(o *orderDetail) *exchange.OrderResult {
	return &exchange.OrderResult{
		ID:        o.OrderID,
		Symbol:    o.Symbol,
		Side:      exchange.OrderSide(strings.ToLower(o.Side)),
		Status:    mapStatus(o.OrderStatus),
		FilledQty: parseDecimal(o.CumExecQty),
		FilledAvg: parseDecimal(o.AvgPrice),
	}
}

func mapSide(s exchange.OrderSide) string {
	if s == exchange.Sell {
		return "Sell"
	}
	return "Buy"
}

func mapStatus(s string) string {
	switch s {
	case "New":
		return "new"
	case "PartiallyFilled":
		return "partially_filled"
	case "Filled":
		return "filled"
	case "Cancelled":
		return "canceled"
	default:
		return s
	}
}

// --- Bybit V5 order types ---

type createOrderRequest struct {
	Category    string `json:"category"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	OrderType   string `json:"orderType"`
	Qty         string `json:"qty"`
	Price       string `json:"price,omitempty"`
	TimeInForce string `json:"timeInForce"`
	ReduceOnly  bool   `json:"reduceOnly,omitempty"`
}

type createOrderResult struct {
	OrderID string `json:"orderId"`
}

type cancelOrderRequest struct {
	Category string `json:"category"`
	OrderID  string `json:"orderId"`
}

type orderListResult struct {
	List []orderDetail `json:"list"`
}

type orderDetail struct {
	OrderID     string `json:"orderId"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	OrderStatus string `json:"orderStatus"`
	CumExecQty  string `json:"cumExecQty"`
	AvgPrice    string `json:"avgPrice"`
	Qty         string `json:"qty"`
	Price       string `json:"price"`
}
