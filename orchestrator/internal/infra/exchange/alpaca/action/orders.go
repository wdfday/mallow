package action

import (
	"context"
	"fmt"
	"log/slog"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"

	"orchestrator/internal/infra/exchange"
)

// PlaceOrder submits a market or limit order via Alpaca SDK.
func (c *Client) PlaceOrder(_ context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	sdk := c.newSDK(creds)

	tif := alpacasdk.Day
	if isCrypto(req.Symbol) {
		tif = alpacasdk.GTC
	}

	orderType := alpacasdk.Market
	if req.Type == exchange.Limit {
		orderType = alpacasdk.Limit
	}

	qty := req.Qty
	sdkReq := alpacasdk.PlaceOrderRequest{
		Symbol:      req.Symbol,
		Qty:         &qty,
		Side:        alpacasdk.Side(req.Side),
		Type:        orderType,
		TimeInForce: tif,
	}

	if orderType == alpacasdk.Limit && req.Price.IsPositive() {
		lp := req.Price
		sdkReq.LimitPrice = &lp
	}

	if req.StopLoss.IsPositive() && req.TakeProfit.IsPositive() {
		sdkReq.OrderClass = alpacasdk.Bracket
		sl := req.StopLoss
		tp := req.TakeProfit
		sdkReq.StopLoss = &alpacasdk.StopLoss{StopPrice: &sl}
		sdkReq.TakeProfit = &alpacasdk.TakeProfit{LimitPrice: &tp}
	} else if req.StopLoss.IsPositive() {
		sdkReq.OrderClass = alpacasdk.OTO
		sl := req.StopLoss
		sdkReq.StopLoss = &alpacasdk.StopLoss{StopPrice: &sl}
	} else if req.TakeProfit.IsPositive() {
		sdkReq.OrderClass = alpacasdk.OTO
		tp := req.TakeProfit
		sdkReq.TakeProfit = &alpacasdk.TakeProfit{LimitPrice: &tp}
	}

	slog.Info("alpaca: placing order", "symbol", req.Symbol, "side", req.Side, "qty", req.Qty)

	order, err := sdk.PlaceOrder(sdkReq)
	if err != nil {
		return nil, fmt.Errorf("alpaca place order: %w", err)
	}
	return mapOrder(order), nil
}

// GetOrder retrieves an order by ID.
func (c *Client) GetOrder(_ context.Context, creds exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	order, err := c.newSDK(creds).GetOrder(orderID)
	if err != nil {
		return nil, fmt.Errorf("alpaca get order: %w", err)
	}
	return mapOrder(order), nil
}

// CancelOrder cancels a single pending order by ID.
func (c *Client) CancelOrder(_ context.Context, creds exchange.Credentials, orderID string) error {
	if err := c.newSDK(creds).CancelOrder(orderID); err != nil {
		return fmt.Errorf("alpaca cancel order: %w", err)
	}
	return nil
}

// GetOrders lists orders with optional filters.
func (c *Client) GetOrders(creds exchange.Credentials, req alpacasdk.GetOrdersRequest) ([]exchange.OrderResult, error) {
	orders, err := c.newSDK(creds).GetOrders(req)
	if err != nil {
		return nil, fmt.Errorf("alpaca get orders: %w", err)
	}
	results := make([]exchange.OrderResult, len(orders))
	for i := range orders {
		results[i] = *mapOrder(&orders[i])
	}
	return results, nil
}

// ReplaceOrder modifies an existing order (qty, price, etc).
func (c *Client) ReplaceOrder(creds exchange.Credentials, orderID string, req alpacasdk.ReplaceOrderRequest) (*exchange.OrderResult, error) {
	order, err := c.newSDK(creds).ReplaceOrder(orderID, req)
	if err != nil {
		return nil, fmt.Errorf("alpaca replace order: %w", err)
	}
	return mapOrder(order), nil
}

// CancelAllOrders cancels every open order.
func (c *Client) CancelAllOrders(creds exchange.Credentials) error {
	if err := c.newSDK(creds).CancelAllOrders(); err != nil {
		return fmt.Errorf("alpaca cancel all orders: %w", err)
	}
	return nil
}

// mapOrder converts an SDK Order to the internal OrderResult.
func mapOrder(o *alpacasdk.Order) *exchange.OrderResult {
	result := &exchange.OrderResult{
		ID:        o.ID,
		Symbol:    o.Symbol,
		Side:      exchange.OrderSide(o.Side),
		Status:    o.Status,
		FilledQty: o.FilledQty,
	}
	if o.FilledAvgPrice != nil {
		result.FilledAvg = *o.FilledAvgPrice
	}
	if o.Qty != nil {
		result.Qty = *o.Qty
	}
	return result
}
