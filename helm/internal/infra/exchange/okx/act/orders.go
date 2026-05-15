package act

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"mallow/helm/internal/infra/exchange"
)

// PlaceOrder routes to spot or futures (SWAP) endpoint based on req.Market.
func (c *Client) PlaceOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	side := "buy"
	if req.Side == exchange.Sell {
		side = "sell"
	}
	ordType := "market"
	if req.Type == exchange.Limit {
		ordType = okxOrdType(req.TIF) // "limit" | "ioc" | "fok" | "post_only"
	}

	tdMode := "cash"
	instID := req.Symbol
	if req.Market == exchange.MarketFutures {
		tdMode = "cross"
	}

	body := placeOrderReq{
		InstID:     instID,
		TdMode:     tdMode,
		Side:       side,
		OrdType:    ordType,
		Sz:         req.Qty.String(),
		ReduceOnly: req.ReduceOnly,
	}

	if req.Market != exchange.MarketFutures && ordType == "market" {
		body.TgtCcy = "base_ccy"
	}
	isLimitVariant := ordType == "limit" || ordType == "ioc" || ordType == "fok" || ordType == "post_only"
	if isLimitVariant && req.Price.IsPositive() {
		body.Px = req.Price.String()
	}

	slog.Info("okx: placing order", "symbol", req.Symbol, "side", side, "qty", req.Qty, "market", req.Market)

	var resp placeOrderResp
	if err := c.doRequest(ctx, creds, http.MethodPost, "/api/v5/trade/order", body, &resp); err != nil {
		return nil, fmt.Errorf("place order: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		msg := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			msg = resp.Data[0].SMsg
		}
		return nil, fmt.Errorf("okx order failed: code=%s msg=%s", resp.Code, msg)
	}

	return &exchange.OrderResult{
		ID:     instID + ":" + resp.Data[0].OrdID,
		Symbol: req.Symbol,
		Side:   req.Side,
		Status: "submitted",
		Qty:    req.Qty,
	}, nil
}

// GetOrder retrieves order status by "instId:ordId" encoded ID.
func (c *Client) GetOrder(ctx context.Context, creds exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	instID, ordID := parseOKXOrderID(orderID)
	path := fmt.Sprintf("/api/v5/trade/order?ordId=%s&instId=%s", ordID, instID)
	var resp getOrderResp
	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, fmt.Errorf("okx get order: code=%s msg=%s", resp.Code, resp.Msg)
	}
	r := orderDataToResult(&resp.Data[0])
	r.ID = orderID
	return r, nil
}

// CancelOrder cancels a pending order by "instId:ordId" encoded ID.
func (c *Client) CancelOrder(ctx context.Context, creds exchange.Credentials, orderID string) error {
	instID, ordID := parseOKXOrderID(orderID)
	body := cancelOrderReq{InstID: instID, OrdID: ordID}
	var resp placeOrderResp
	if err := c.doRequest(ctx, creds, http.MethodPost, "/api/v5/trade/cancel-order", body, &resp); err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("okx cancel: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// GetPendingOrders returns all open orders for an optional instrument.
func (c *Client) GetPendingOrders(ctx context.Context, creds exchange.Credentials, instID string) ([]exchange.OrderResult, error) {
	path := "/api/v5/trade/orders-pending"
	if instID != "" {
		path += "?instId=" + instID
	}
	var resp getOrderResp
	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("get pending orders: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx pending orders: code=%s msg=%s", resp.Code, resp.Msg)
	}
	results := make([]exchange.OrderResult, len(resp.Data))
	for i := range resp.Data {
		results[i] = *orderDataToResult(&resp.Data[i])
		results[i].ID = resp.Data[i].InstID + ":" + resp.Data[i].OrdID
	}
	return results, nil
}

// AmendOrder modifies price or qty of an existing live order.
func (c *Client) AmendOrder(ctx context.Context, creds exchange.Credentials, instID, orderID, newSz, newPx string) error {
	_, ordID := parseOKXOrderID(orderID) // strip "instID:" prefix if present
	body := map[string]string{"instId": instID, "ordId": ordID}
	if newSz != "" {
		body["newSz"] = newSz
	}
	if newPx != "" {
		body["newPx"] = newPx
	}
	var resp placeOrderResp
	if err := c.doRequest(ctx, creds, http.MethodPost, "/api/v5/trade/amend-order", body, &resp); err != nil {
		return fmt.Errorf("amend order: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("okx amend: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// ── Exit / bracket orders ─────────────────────────────────────────────────────

// PlaceExitOrders places exchange-side SL/TP algo orders after an entry fill.
// Uses ordType="oco" when both are set, "conditional" when only one is set.
func (c *Client) PlaceExitOrders(ctx context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	side := "sell"
	if req.Side == exchange.Buy {
		side = "buy"
	}
	tdMode := "cash"
	if req.Market == exchange.MarketFutures {
		tdMode = "cross"
	}
	reduceOnly := req.Market == exchange.MarketFutures

	hasSL := req.StopLoss.IsPositive()
	hasTP := req.TakeProfit.IsPositive()
	if !hasSL && !hasTP {
		return &exchange.ExitOrderResult{}, nil
	}

	body := algoOrderReq{
		InstID:     req.Symbol,
		TdMode:     tdMode,
		Side:       side,
		Sz:         req.Qty.String(),
		ReduceOnly: reduceOnly,
	}

	if hasSL && hasTP {
		body.OrdType = "oco"
		body.TpTriggerPx = req.TakeProfit.String()
		body.TpOrdPx = "-1" // market execution
		body.SlTriggerPx = req.StopLoss.String()
		body.SlOrdPx = "-1" // market execution
	} else if hasSL {
		body.OrdType = "conditional"
		body.SlTriggerPx = req.StopLoss.String()
		body.SlOrdPx = "-1"
	} else {
		body.OrdType = "conditional"
		body.TpTriggerPx = req.TakeProfit.String()
		body.TpOrdPx = "-1"
	}

	slog.Info("okx: placing exit algo order", "symbol", req.Symbol, "side", side, "ordType", body.OrdType,
		"sl", req.StopLoss, "tp", req.TakeProfit)

	var resp algoOrderResp
	if err := c.doRequest(ctx, creds, http.MethodPost, "/api/v5/trade/order-algo", body, &resp); err != nil {
		return nil, fmt.Errorf("okx place exit orders: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		msg := resp.Msg
		if len(resp.Data) > 0 && resp.Data[0].SMsg != "" {
			msg = resp.Data[0].SMsg
		}
		return nil, fmt.Errorf("okx algo order failed: code=%s msg=%s", resp.Code, msg)
	}
	return &exchange.ExitOrderResult{
		OrderIDs: []string{req.Symbol + ":" + resp.Data[0].AlgoID},
	}, nil
}

// okxOrdType maps canonical TIF to OKX ordType for limit orders.
// OKX embeds TIF in the order type field rather than a separate parameter.
func okxOrdType(tif exchange.TimeInForce) string {
	switch tif {
	case exchange.TIFIOC:
		return "ioc"
	case exchange.TIFFOK:
		return "fok"
	case exchange.TIFDay:
		return "post_only" // maker-only; closest to session-scoped on OKX
	default:
		return "limit" // GTC is the default for limit orders
	}
}
