package ibkr

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"orchestrator/internal/infra/exchange"
)

// PlaceOrder submits an order via IBKR Client Portal API.
// Symbol should be a conid (contract ID) as string.
func (c *Client) PlaceOrder(ctx context.Context, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	side := "BUY"
	if req.Side == exchange.Sell {
		side = "SELL"
	}
	orderType := "MKT"
	if req.Type == exchange.Limit {
		orderType = "LMT"
	}

	conid, _ := strconv.Atoi(req.Symbol)
	order := ibkrOrderRequest{
		ConID:     conid,
		Side:      side,
		OrderType: orderType,
		Quantity:  req.Qty,
		Tif:       "DAY",
	}
	if orderType == "LMT" {
		order.Price = req.Price
	}

	body := map[string]any{"orders": []ibkrOrderRequest{order}}

	slog.Info("ibkr: placing order", "conid", req.Symbol, "side", side, "qty", req.Qty)

	path := fmt.Sprintf("/iserver/account/%s/orders", c.cfg.AccountID)
	var resp []ibkrOrderResponse
	if err := c.doRequest(ctx, http.MethodPost, path, body, &resp); err != nil {
		return nil, fmt.Errorf("ibkr place order: %w", err)
	}
	if len(resp) == 0 {
		return nil, fmt.Errorf("ibkr: empty order response")
	}

	return &exchange.OrderResult{
		ID:     resp[0].OrderID,
		Symbol: req.Symbol,
		Side:   req.Side,
		Status: resp[0].OrderStatus,
		Qty:    req.Qty,
	}, nil
}

// GetOrder retrieves an order by ID.
func (c *Client) GetOrder(ctx context.Context, orderID string) (*exchange.OrderResult, error) {
	path := fmt.Sprintf("/iserver/account/order/status/%s", orderID)
	var resp ibkrOrderStatusResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("ibkr get order: %w", err)
	}
	return &exchange.OrderResult{
		ID:        resp.OrderID,
		Symbol:    fmt.Sprintf("%d", resp.ConID),
		Side:      exchange.OrderSide(mapIBKRSide(resp.Side)),
		Status:    resp.Status,
		FilledQty: resp.FilledQty,
		FilledAvg: resp.AvgPrice,
	}, nil
}

// GetLiveOrders returns all live/working orders.
func (c *Client) GetLiveOrders(ctx context.Context) ([]exchange.OrderResult, error) {
	var resp struct {
		Orders []ibkrLiveOrder `json:"orders"`
	}
	if err := c.doRequest(ctx, http.MethodGet, "/iserver/account/orders", nil, &resp); err != nil {
		return nil, fmt.Errorf("ibkr live orders: %w", err)
	}

	results := make([]exchange.OrderResult, len(resp.Orders))
	for i, o := range resp.Orders {
		results[i] = exchange.OrderResult{
			ID:        o.OrderID,
			Symbol:    fmt.Sprintf("%d", o.ConID),
			Side:      exchange.OrderSide(mapIBKRSide(o.Side)),
			Status:    o.Status,
			FilledQty: o.FilledQty,
		}
	}
	return results, nil
}

// CancelOrder cancels a pending order.
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	path := fmt.Sprintf("/iserver/account/%s/order/%s", c.cfg.AccountID, orderID)
	if err := c.doRequest(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return fmt.Errorf("ibkr cancel order: %w", err)
	}
	return nil
}

func mapIBKRSide(s string) string {
	if s == "SELL" || s == "S" {
		return "sell"
	}
	return "buy"
}

// --- IBKR order types ---

type ibkrOrderRequest struct {
	ConID     int     `json:"conid"`
	Side      string  `json:"side"`
	OrderType string  `json:"orderType"`
	Quantity  float64 `json:"quantity"`
	Price     float64 `json:"price,omitempty"`
	Tif       string  `json:"tif"`
}

type ibkrOrderResponse struct {
	OrderID     string `json:"order_id"`
	OrderStatus string `json:"order_status"`
}

type ibkrOrderStatusResponse struct {
	OrderID   string  `json:"order_id"`
	ConID     int     `json:"conid"`
	Side      string  `json:"side"`
	Status    string  `json:"order_status"`
	FilledQty float64 `json:"filled_quantity"`
	AvgPrice  float64 `json:"average_price"`
}

type ibkrLiveOrder struct {
	OrderID   string  `json:"orderId"`
	ConID     int     `json:"conid"`
	Side      string  `json:"side"`
	Status    string  `json:"status"`
	FilledQty float64 `json:"filledQuantity"`
}
