package action

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"orchestrator/internal/infra/exchange"
)

// parseOKXOrderID splits the "INSTID:ordId" format used by PlaceOrder.
// OKX REST requires instId for GetOrder and CancelOrder.
func parseOKXOrderID(orderID string) (instID, ordID string) {
	parts := strings.SplitN(orderID, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "_", orderID // fallback: unknown instId
}

// PlaceOrder routes to spot or futures (SWAP) endpoint based on req.Market.
// Returns ID encoded as "instId:ordId" so GetOrder/CancelOrder can pass instId back.
func (c *Client) PlaceOrder(ctx context.Context, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	side := "buy"
	if req.Side == exchange.Sell {
		side = "sell"
	}
	ordType := "market"
	if req.Type == exchange.Limit {
		ordType = "limit"
	}

	tdMode := "cash"
	instID := req.Symbol
	if req.Market == exchange.MarketFutures {
		tdMode = "cross"
	}

	body := placeOrderRequest{
		InstID:     instID,
		TdMode:     tdMode,
		Side:       side,
		OrdType:    ordType,
		Sz:         fmt.Sprintf("%g", req.Qty),
		ReduceOnly: req.ReduceOnly,
	}

	// OKX spot market buy: default sz unit is quote currency (USDT).
	// Set tgtCcy=base_ccy so sz is always base currency (e.g. BTC).
	if req.Market != exchange.MarketFutures && ordType == "market" {
		body.TgtCcy = "base_ccy"
	}
	if ordType == "limit" && req.Price > 0 {
		body.Px = fmt.Sprintf("%g", req.Price)
	}

	// Bracket order: attach TP/SL as algo legs.
	if req.TakeProfit > 0 || req.StopLoss > 0 {
		leg := attachAlgoOrd{}
		if req.TakeProfit > 0 {
			leg.TpTriggerPx = fmt.Sprintf("%g", req.TakeProfit)
			leg.TpOrdPx = "-1" // market TP
		}
		if req.StopLoss > 0 {
			leg.SlTriggerPx = fmt.Sprintf("%g", req.StopLoss)
			leg.SlOrdPx = "-1" // market SL
		}
		body.AttachAlgoOrds = []attachAlgoOrd{leg}
	}

	slog.Info("okx: placing order", "symbol", req.Symbol, "side", side, "qty", req.Qty, "market", req.Market)

	var resp placeOrderResponse
	if err := c.doRequest(ctx, http.MethodPost, "/api/v5/trade/order", body, &resp); err != nil {
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
func (c *Client) GetOrder(ctx context.Context, orderID string) (*exchange.OrderResult, error) {
	instID, ordID := parseOKXOrderID(orderID)
	path := fmt.Sprintf("/api/v5/trade/order?ordId=%s&instId=%s", ordID, instID)
	var resp getOrderResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, fmt.Errorf("okx get order: code=%s msg=%s", resp.Code, resp.Msg)
	}
	r := mapOrderDetail(&resp.Data[0])
	r.ID = orderID // preserve encoded format
	return r, nil
}

// CancelOrder cancels a pending order by "instId:ordId" encoded ID.
func (c *Client) CancelOrder(ctx context.Context, orderID string) error {
	instID, ordID := parseOKXOrderID(orderID)
	body := cancelOrderRequest{InstID: instID, OrdID: ordID}
	var resp placeOrderResponse
	if err := c.doRequest(ctx, http.MethodPost, "/api/v5/trade/cancel-order", body, &resp); err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("okx cancel: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// GetPendingOrders returns all open orders for an optional instrument.
func (c *Client) GetPendingOrders(ctx context.Context, instID string) ([]exchange.OrderResult, error) {
	path := "/api/v5/trade/orders-pending"
	if instID != "" {
		path += "?instId=" + instID
	}
	var resp getOrderResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("get pending orders: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx pending orders: code=%s msg=%s", resp.Code, resp.Msg)
	}
	results := make([]exchange.OrderResult, len(resp.Data))
	for i := range resp.Data {
		results[i] = *mapOrderDetail(&resp.Data[i])
		results[i].ID = resp.Data[i].InstID + ":" + resp.Data[i].OrdID
	}
	return results, nil
}

// AmendOrder modifies price or qty of an existing live order.
func (c *Client) AmendOrder(ctx context.Context, instID, orderID, newSz, newPx string) error {
	body := map[string]string{"instId": instID, "ordId": orderID}
	if newSz != "" {
		body["newSz"] = newSz
	}
	if newPx != "" {
		body["newPx"] = newPx
	}
	var resp placeOrderResponse
	if err := c.doRequest(ctx, http.MethodPost, "/api/v5/trade/amend-order", body, &resp); err != nil {
		return fmt.Errorf("amend order: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("okx amend: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

func mapOrderDetail(d *orderDetailData) *exchange.OrderResult {
	return &exchange.OrderResult{
		ID:        d.InstID + ":" + d.OrdID,
		Symbol:    d.InstID,
		Side:      exchange.OrderSide(d.Side),
		Status:    mapOKXStatus(d.State),
		Qty:       parseFloat(d.Sz),
		FilledQty: parseFloat(d.AccFillSz),
		FilledAvg: parseFloat(d.AvgPx),
	}
}

func mapOKXStatus(state string) string {
	switch state {
	case "live":
		return "new"
	case "partially_filled":
		return "partially_filled"
	case "filled":
		return "filled"
	case "canceled":
		return "canceled"
	default:
		return state
	}
}

// ── Request/Response types ────────────────────────────────────────────────────

type attachAlgoOrd struct {
	TpTriggerPx string `json:"tpTriggerPx,omitempty"`
	TpOrdPx     string `json:"tpOrdPx,omitempty"`
	SlTriggerPx string `json:"slTriggerPx,omitempty"`
	SlOrdPx     string `json:"slOrdPx,omitempty"`
}

type placeOrderRequest struct {
	InstID         string          `json:"instId"`
	TdMode         string          `json:"tdMode"`
	Side           string          `json:"side"`
	OrdType        string          `json:"ordType"`
	Sz             string          `json:"sz"`
	Px             string          `json:"px,omitempty"`
	TgtCcy         string          `json:"tgtCcy,omitempty"`
	ReduceOnly     bool            `json:"reduceOnly,omitempty"`
	AttachAlgoOrds []attachAlgoOrd `json:"attachAlgoOrds,omitempty"`
}

type cancelOrderRequest struct {
	InstID string `json:"instId,omitempty"`
	OrdID  string `json:"ordId"`
}

type placeOrderResponse struct {
	okxResponse
	Data []struct {
		OrdID string `json:"ordId"`
		SCode string `json:"sCode"`
		SMsg  string `json:"sMsg"`
	} `json:"data"`
}

type getOrderResponse struct {
	okxResponse
	Data []orderDetailData `json:"data"`
}

type orderDetailData struct {
	OrdID     string `json:"ordId"`
	InstID    string `json:"instId"`
	Side      string `json:"side"`
	State     string `json:"state"`
	AccFillSz string `json:"accFillSz"`
	AvgPx     string `json:"avgPx"`
	Sz        string `json:"sz"`
	Px        string `json:"px"`
}
