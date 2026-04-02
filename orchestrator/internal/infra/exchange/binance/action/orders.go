package action

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	gobinance "github.com/adshao/go-binance/v2"
	"github.com/adshao/go-binance/v2/futures"

	"orchestrator/internal/infra/exchange"
)

// PlaceOrder routes to spot or futures endpoint based on req.Market.
func (c *Client) PlaceOrder(ctx context.Context, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	if req.Market == exchange.MarketFutures {
		return c.placeFuturesOrder(ctx, req)
	}
	return c.placeSpotOrder(ctx, req)
}

func (c *Client) placeSpotOrder(ctx context.Context, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	side := gobinance.SideTypeBuy
	if req.Side == exchange.Sell {
		side = gobinance.SideTypeSell
	}
	orderType := gobinance.OrderTypeMarket
	if req.Type == exchange.Limit {
		orderType = gobinance.OrderTypeLimit
	}

	svc := c.spot.NewCreateOrderService().
		Symbol(req.Symbol).
		Side(side).
		Type(orderType).
		Quantity(strconv.FormatFloat(req.Qty, 'f', -1, 64))

	if orderType == gobinance.OrderTypeLimit {
		svc = svc.TimeInForce(gobinance.TimeInForceTypeGTC).
			Price(strconv.FormatFloat(req.Price, 'f', -1, 64))
	}

	slog.Info("binance: placing spot order", "symbol", req.Symbol, "side", req.Side, "qty", req.Qty)
	resp, err := svc.Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance spot place order: %w", err)
	}
	filledQty := parseFloat(resp.ExecutedQuantity)
	quoteQty := parseFloat(resp.CummulativeQuoteQuantity)
	var filledAvg float64
	if filledQty > 0 {
		filledAvg = quoteQty / filledQty
	}
	return &exchange.OrderResult{
		ID:        req.Symbol + ":" + strconv.FormatInt(resp.OrderID, 10),
		Symbol:    resp.Symbol,
		Side:      req.Side,
		Status:    string(resp.Status),
		Qty:       parseFloat(resp.OrigQuantity),
		FilledQty: filledQty,
		FilledAvg: filledAvg,
	}, nil
}

func (c *Client) placeFuturesOrder(ctx context.Context, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	side := futures.SideTypeBuy
	if req.Side == exchange.Sell {
		side = futures.SideTypeSell
	}
	orderType := futures.OrderTypeMarket
	if req.Type == exchange.Limit {
		orderType = futures.OrderTypeLimit
	}

	svc := c.fut.NewCreateOrderService().
		Symbol(req.Symbol).
		Side(side).
		Type(orderType).
		Quantity(fmt.Sprintf("%g", req.Qty)).
		ReduceOnly(req.ReduceOnly)

	if orderType == futures.OrderTypeLimit {
		svc = svc.TimeInForce(futures.TimeInForceTypeGTC).
			Price(fmt.Sprintf("%g", req.Price))
	}

	slog.Info("binance: placing futures order", "symbol", req.Symbol, "side", req.Side, "qty", req.Qty, "reduce_only", req.ReduceOnly)
	resp, err := svc.Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance futures place order: %w", err)
	}
	return &exchange.OrderResult{
		ID:        strconv.FormatInt(resp.OrderID, 10),
		Symbol:    resp.Symbol,
		Side:      req.Side,
		Status:    string(resp.Status),
		Qty:       parseFloat(resp.OrigQuantity),
		FilledQty: parseFloat(resp.ExecutedQuantity),
	}, nil
}

// parseSpotOrderID splits the "SYMBOL:id" format returned by PlaceOrder.
// Binance spot requires both symbol and numeric ID to query or cancel an order.
func parseSpotOrderID(orderID string) (symbol string, oid int64, err error) {
	parts := strings.SplitN(orderID, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("binance: invalid order ID %q (expected SYMBOL:id)", orderID)
	}
	oid, err = strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("binance: invalid numeric order ID in %q: %w", orderID, err)
	}
	return parts[0], oid, nil
}

// GetOrder retrieves spot order status. For futures orders use GetFuturesOrder.
func (c *Client) GetOrder(ctx context.Context, orderID string) (*exchange.OrderResult, error) {
	symbol, oid, err := parseSpotOrderID(orderID)
	if err != nil {
		return nil, err
	}
	resp, err := c.spot.NewGetOrderService().Symbol(symbol).OrderID(oid).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance get order: %w", err)
	}
	filledQty := parseFloat(resp.ExecutedQuantity)
	quoteQty := parseFloat(resp.CummulativeQuoteQuantity)
	var filledAvg float64
	if filledQty > 0 {
		filledAvg = quoteQty / filledQty
	}
	return &exchange.OrderResult{
		ID:        orderID,
		Symbol:    resp.Symbol,
		Side:      exchange.OrderSide(mapSide(resp.Side)),
		Status:    string(resp.Status),
		Qty:       parseFloat(resp.OrigQuantity),
		FilledQty: filledQty,
		FilledAvg: filledAvg,
	}, nil
}

// CancelOrder cancels a spot order by ID.
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	symbol, oid, err := parseSpotOrderID(orderID)
	if err != nil {
		return err
	}
	_, err = c.spot.NewCancelOrderService().Symbol(symbol).OrderID(oid).Do(ctx)
	if err != nil {
		return fmt.Errorf("binance cancel order: %w", err)
	}
	return nil
}

// GetFuturesOrder retrieves futures order status.
func (c *Client) GetFuturesOrder(ctx context.Context, symbol string, orderID int64) (*exchange.OrderResult, error) {
	resp, err := c.fut.NewGetOrderService().Symbol(symbol).OrderID(orderID).Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("binance futures get order: %w", err)
	}
	return &exchange.OrderResult{
		ID:        strconv.FormatInt(resp.OrderID, 10),
		Symbol:    resp.Symbol,
		Side:      exchange.OrderSide(string(resp.Side)),
		Status:    string(resp.Status),
		Qty:       parseFloat(resp.OrigQuantity),
		FilledQty: parseFloat(resp.ExecutedQuantity),
	}, nil
}

// CancelFuturesOrder cancels a futures order.
func (c *Client) CancelFuturesOrder(ctx context.Context, symbol string, orderID int64) error {
	_, err := c.fut.NewCancelOrderService().Symbol(symbol).OrderID(orderID).Do(ctx)
	if err != nil {
		return fmt.Errorf("binance futures cancel order: %w", err)
	}
	return nil
}

func mapSide(s gobinance.SideType) string {
	if s == gobinance.SideTypeSell {
		return "sell"
	}
	return "buy"
}
