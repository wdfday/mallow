package act

import (
	"context"
	"fmt"
	"log/slog"

	alpacasdk "github.com/alpacahq/alpaca-trade-api-go/v3/alpaca"

	"mallow/helm/internal/infra/exchange"
)

// PlaceOrder submits a market or limit order via Alpaca SDK.
func (c *Client) PlaceOrder(_ context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	sdk := c.newSDK(creds)

	tif := alpacaTIF(req.TIF, req.Symbol)

	orderType := alpacasdk.Market
	if req.Type == exchange.Limit {
		orderType = alpacasdk.Limit
	}

	qty := req.Qty
	sdkReq := alpacasdk.PlaceOrderRequest{
		Symbol:        req.Symbol,
		Qty:           &qty,
		Side:          alpacasdk.Side(req.Side),
		Type:          orderType,
		TimeInForce:   tif,
		ClientOrderID: req.ClientOrderID,
	}

	if orderType == alpacasdk.Limit && req.Price.IsPositive() {
		lp := req.Price
		sdkReq.LimitPrice = &lp
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

// GetOrderByClientOrderID looks up an order by its client_order_id. symbol/market are
// unused — Alpaca client order ids are account-unique across its single equities venue.
// Returns nil when not found or on lookup failure. See CLIENT_ORDER_ID.md.
func (c *Client) GetOrderByClientOrderID(_ context.Context, creds exchange.Credentials, _ string, _ exchange.MarketKind, clid string) (*exchange.OrderResult, error) {
	order, err := c.newSDK(creds).GetOrderByClientOrderID(clid)
	if err != nil || order == nil {
		return nil, nil
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

// ── Exit / bracket orders ─────────────────────────────────────────────────────

// PlaceExitOrders places SL/TP orders post-fill.
// Alpaca does not support attaching exits to an already-filled order, so this
// places independent stop (SL) and limit (TP) orders. Both are reduce-only by
// intent — the local exit monitor cancels the survivor when one fires.
func (c *Client) PlaceExitOrders(_ context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	sdk := c.newSDK(creds)
	side := alpacasdk.Side(req.Side)

	var ids []string

	if req.StopLoss.IsPositive() {
		sl := req.StopLoss
		stopReq := alpacasdk.PlaceOrderRequest{
			Symbol:      req.Symbol,
			Qty:         &req.Qty,
			Side:        side,
			Type:        alpacasdk.Stop,
			TimeInForce: alpacasdk.GTC,
			StopPrice:   &sl,
		}
		slog.Info("alpaca: placing SL exit", "symbol", req.Symbol, "side", side, "sl", sl)
		order, err := sdk.PlaceOrder(stopReq)
		if err != nil {
			return nil, fmt.Errorf("alpaca SL exit: %w", err)
		}
		ids = append(ids, order.ID)
	}

	if req.TakeProfit.IsPositive() {
		tp := req.TakeProfit
		tpReq := alpacasdk.PlaceOrderRequest{
			Symbol:      req.Symbol,
			Qty:         &req.Qty,
			Side:        side,
			Type:        alpacasdk.Limit,
			TimeInForce: alpacasdk.GTC,
			LimitPrice:  &tp,
		}
		slog.Info("alpaca: placing TP exit", "symbol", req.Symbol, "side", side, "tp", tp)
		order, err := sdk.PlaceOrder(tpReq)
		if err != nil {
			return nil, fmt.Errorf("alpaca TP exit: %w", err)
		}
		ids = append(ids, order.ID)
	}

	return &exchange.ExitOrderResult{OrderIDs: ids}, nil
}

// alpacaTIF maps canonical TIF to Alpaca TimeInForce.
// Alpaca default: Day for equities, GTC for crypto.
func alpacaTIF(tif exchange.TimeInForce, symbol string) alpacasdk.TimeInForce {
	switch tif {
	case exchange.TIFGTC:
		return alpacasdk.GTC
	case exchange.TIFDay:
		return alpacasdk.Day
	case exchange.TIFIOC:
		return alpacasdk.IOC
	case exchange.TIFFOK:
		return alpacasdk.FOK
	default:
		if isCrypto(symbol) {
			return alpacasdk.GTC
		}
		return alpacasdk.Day
	}
}
