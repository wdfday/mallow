package action

import (
	"context"
	"fmt"
	"log/slog"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"
	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
)

// PlaceOrder submits a market or limit order via Alpaca SDK.
func (c *Client) PlaceOrder(_ context.Context, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	tif := alpacasdk.Day
	if isCrypto(req.Symbol) {
		tif = alpacasdk.GTC
	}

	orderType := alpacasdk.Market
	if req.Type == exchange.Limit {
		orderType = alpacasdk.Limit
	}

	qty := decimal.NewFromFloat(req.Qty)
	sdkReq := alpacasdk.PlaceOrderRequest{
		Symbol:      req.Symbol,
		Qty:         &qty,
		Side:        alpacasdk.Side(req.Side),
		Type:        orderType,
		TimeInForce: tif,
	}

	if orderType == alpacasdk.Limit && req.Price > 0 {
		lp := decimal.NewFromFloat(req.Price)
		sdkReq.LimitPrice = &lp
	}

	if req.StopLoss > 0 && req.TakeProfit > 0 {
		sdkReq.OrderClass = alpacasdk.Bracket
		sl := decimal.NewFromFloat(req.StopLoss)
		tp := decimal.NewFromFloat(req.TakeProfit)
		sdkReq.StopLoss = &alpacasdk.StopLoss{StopPrice: &sl}
		sdkReq.TakeProfit = &alpacasdk.TakeProfit{LimitPrice: &tp}
	} else if req.StopLoss > 0 {
		sdkReq.OrderClass = alpacasdk.OTO
		sl := decimal.NewFromFloat(req.StopLoss)
		sdkReq.StopLoss = &alpacasdk.StopLoss{StopPrice: &sl}
	} else if req.TakeProfit > 0 {
		sdkReq.OrderClass = alpacasdk.OTO
		tp := decimal.NewFromFloat(req.TakeProfit)
		sdkReq.TakeProfit = &alpacasdk.TakeProfit{LimitPrice: &tp}
	}

	slog.Info("alpaca: placing order", "symbol", req.Symbol, "side", req.Side, "qty", req.Qty)

	order, err := c.sdk.PlaceOrder(sdkReq)
	if err != nil {
		return nil, fmt.Errorf("alpaca place order: %w", err)
	}
	return mapOrder(order), nil
}

// GetOrder retrieves an order by ID.
func (c *Client) GetOrder(_ context.Context, orderID string) (*exchange.OrderResult, error) {
	order, err := c.sdk.GetOrder(orderID)
	if err != nil {
		return nil, fmt.Errorf("alpaca get order: %w", err)
	}
	return mapOrder(order), nil
}

// GetOrderByClientID retrieves an order by the client-provided order ID.
func (c *Client) GetOrderByClientID(clientOrderID string) (*exchange.OrderResult, error) {
	order, err := c.sdk.GetOrderByClientOrderID(clientOrderID)
	if err != nil {
		return nil, fmt.Errorf("alpaca get order by client id: %w", err)
	}
	return mapOrder(order), nil
}

// GetOrders lists orders with optional filters.
func (c *Client) GetOrders(req alpacasdk.GetOrdersRequest) ([]exchange.OrderResult, error) {
	orders, err := c.sdk.GetOrders(req)
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
func (c *Client) ReplaceOrder(orderID string, req alpacasdk.ReplaceOrderRequest) (*exchange.OrderResult, error) {
	order, err := c.sdk.ReplaceOrder(orderID, req)
	if err != nil {
		return nil, fmt.Errorf("alpaca replace order: %w", err)
	}
	return mapOrder(order), nil
}

// CancelOrder cancels a single pending order by ID.
func (c *Client) CancelOrder(_ context.Context, orderID string) error {
	if err := c.sdk.CancelOrder(orderID); err != nil {
		return fmt.Errorf("alpaca cancel order: %w", err)
	}
	return nil
}

// CancelAllOrders cancels every open order.
func (c *Client) CancelAllOrders() error {
	if err := c.sdk.CancelAllOrders(); err != nil {
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
		FilledQty: o.FilledQty.InexactFloat64(),
	}
	if o.FilledAvgPrice != nil {
		result.FilledAvg = o.FilledAvgPrice.InexactFloat64()
	}
	if o.Qty != nil {
		result.Qty = o.Qty.InexactFloat64()
	}
	return result
}
