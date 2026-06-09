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
		if req.MarginMode == "isolated" {
			tdMode = "isolated"
		}
	}

	body := placeOrderReq{
		InstID:     instID,
		TdMode:     tdMode,
		Side:       side,
		OrdType:    ordType,
		Sz:         req.Qty.String(),
		ClOrdID:    req.ClientOrderID,
		ReduceOnly: req.ReduceOnly,
	}

	if req.Market != exchange.MarketFutures && ordType == "market" {
		if req.QuoteQty.IsPositive() {
			body.TgtCcy = "quote_ccy"
			body.Sz = req.QuoteQty.String()
		} else {
			body.TgtCcy = "base_ccy"
		}
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
	// OKX may return outer code="0" but per-order sCode != "0" when the trading
	// engine rejects the order after initial validation (e.g. insufficient balance
	// detected post-routing). In that case OrdID is empty — treat as a placement error.
	if d := resp.Data[0]; d.SCode != "" && d.SCode != "0" {
		return nil, fmt.Errorf("okx order rejected: sCode=%s sMsg=%s", d.SCode, d.SMsg)
	}

	return &exchange.OrderResult{
		ID:            instID + ":" + resp.Data[0].OrdID,
		ClientOrderID: resp.Data[0].ClOrdID,
		Symbol:        req.Symbol,
		Side:          req.Side,
		Status:        "submitted",
		Qty:           req.Qty,
	}, nil
}

// GetOrder retrieves order status by composite ID.
//
// Regular orders use "INSTID:ordId" and query /api/v5/trade/order.
// Algo orders use "INSTID:A:algoId" and query /api/v5/trade/orders-algo.
func (c *Client) GetOrder(ctx context.Context, creds exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	instID, ordID, isAlgo := parseOKXOrderID(orderID)
	if isAlgo {
		return c.getAlgoOrder(ctx, creds, orderID, instID, ordID)
	}
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

// algoOrderData is the shared struct for both active and history algo order responses.
type algoOrderData struct {
	AlgoId     string `json:"algoId"`
	InstId     string `json:"instId"`
	Side       string `json:"side"`
	OrdType    string `json:"ordType"`
	State      string `json:"state"`
	Sz         string `json:"sz"`
	ActualSz   string `json:"actualSz"`
	ActualPx   string `json:"actualPx"`
	ActualSide string `json:"actualSide"` // "sl" or "tp" — which leg triggered (history only)
}

// getAlgoOrder fetches an algo (OCO/conditional) order by algoId.
//
// OKX exposes three relevant endpoints:
//
//	GET /api/v5/trade/order-algo          — "Get Algo Order Details": single-order
//	                                        lookup by algoId; no ordType required;
//	                                        returns any state (live, effective, canceled).
//	GET /api/v5/trade/orders-algo-pending — active (live) algo orders list; ordType required.
//	GET /api/v5/trade/orders-algo-history — triggered/expired history; ordType required.
//
// Strategy:
//  1. Try the details endpoint first — authoritative, no ordType guessing.
//  2. If that returns nothing (edge-case: very recently placed order not yet indexed),
//     fall back to the pending list with both ordType variants.
//  3. Fall back to history (ordType variants) so we can classify fills that WS missed.
func (c *Client) getAlgoOrder(ctx context.Context, creds exchange.Credentials, orderID, instID, algoID string) (*exchange.OrderResult, error) {
	type algoResp struct {
		okxEnvelope
		Data []algoOrderData `json:"data"`
	}

	toResult := func(d algoOrderData) *exchange.OrderResult {
		r := &exchange.OrderResult{
			ID:        orderID,
			Symbol:    d.InstId,
			Side:      exchange.OrderSide(d.Side),
			Status:    mapAlgoOrderStatus(d.State),
			Qty:       parseDecimal(d.Sz),
			FilledQty: parseDecimal(d.ActualSz),
			FilledAvg: parseDecimal(d.ActualPx),
		}
		// actualSide ("sl"/"tp") is only populated in history after the order triggers.
		// Store it in the Tag field so applyBracketStates can log which leg fired.
		if d.ActualSide != "" {
			r.Tag = d.ActualSide
		}
		return r
	}

	// ── 1. Details endpoint (primary) ─────────────────────────────────────────
	// GET /api/v5/trade/order-algo?algoId=xxx — works for any state, no ordType.
	// This is the canonical single-order lookup; the list endpoints below are
	// fallbacks for the rare case where this returns empty.
	detailPath := fmt.Sprintf("/api/v5/trade/order-algo?algoId=%s", algoID)
	var detailResp algoResp
	if err := c.doRequest(ctx, creds, http.MethodGet, detailPath, nil, &detailResp); err == nil &&
		detailResp.Code == "0" && len(detailResp.Data) > 0 {
		return toResult(detailResp.Data[0]), nil
	}

	// ── 2. Active (pending) list fallback ─────────────────────────────────────
	// /api/v5/trade/orders-algo-pending requires ordType; try both OCO and
	// conditional. Used when the details endpoint is slow to index a new order.
	for _, ordType := range []string{"oco", "conditional"} {
		activePath := fmt.Sprintf("/api/v5/trade/orders-algo-pending?ordType=%s&algoId=%s&instId=%s", ordType, algoID, instID)
		var activeResp algoResp
		if err := c.doRequest(ctx, creds, http.MethodGet, activePath, nil, &activeResp); err == nil &&
			activeResp.Code == "0" && len(activeResp.Data) > 0 {
			return toResult(activeResp.Data[0]), nil
		}
	}

	// ── 3. History fallback ───────────────────────────────────────────────────
	// Once triggered or expired the order leaves pending and appears here.
	// 51603 "Order does not exist" means this ordType has no record — try next.
	for _, ordType := range []string{"oco", "conditional"} {
		histPath := fmt.Sprintf("/api/v5/trade/orders-algo-history?algoId=%s&instId=%s&ordType=%s", algoID, instID, ordType)
		var histResp algoResp
		if err := c.doRequest(ctx, creds, http.MethodGet, histPath, nil, &histResp); err != nil {
			return nil, fmt.Errorf("get algo order history (%s): %w", ordType, err)
		}
		if histResp.Code != "0" {
			if histResp.Code == "51603" {
				continue // not in this ordType's history — try next
			}
			return nil, fmt.Errorf("okx get algo order history: code=%s msg=%s", histResp.Code, histResp.Msg)
		}
		if len(histResp.Data) > 0 {
			return toResult(histResp.Data[0]), nil
		}
	}
	// Not found anywhere — definitively gone (triggered, expired, or purged).
	return &exchange.OrderResult{ID: orderID, Status: "not_found"}, nil
}

// mapAlgoOrderStatus maps OKX algo order state to canonical order status strings.
func mapAlgoOrderStatus(state string) string {
	switch state {
	case "live":
		return "new"
	case "effective", "partially_effective":
		return "filled"
	case "canceled", "order_failed":
		return "canceled"
	default:
		return state
	}
}

// GetOrderByClientOrderID looks up an order by clOrdId + instId. OKX's unified order
// endpoint covers spot and derivatives alike, so market is unused. Returns nil when no
// such order exists or the lookup fails. See CLIENT_ORDER_ID.md.
func (c *Client) GetOrderByClientOrderID(ctx context.Context, creds exchange.Credentials, symbol string, _ exchange.MarketKind, clid string) (*exchange.OrderResult, error) {
	path := fmt.Sprintf("/api/v5/trade/order?clOrdId=%s&instId=%s", clid, symbol)
	var resp getOrderResp
	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, nil
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, nil
	}
	return orderDataToResult(&resp.Data[0]), nil
}

// CancelOrder cancels a pending order.
//
// Regular orders use "INSTID:ordId" and call /api/v5/trade/cancel-order.
// Algo orders use "INSTID:A:algoId" and call /api/v5/trade/cancel-algos.
// Returns nil when the order is already cancelled or filled (idempotent).
func (c *Client) CancelOrder(ctx context.Context, creds exchange.Credentials, orderID string) error {
	instID, ordID, isAlgo := parseOKXOrderID(orderID)
	if isAlgo {
		return c.cancelAlgoOrder(ctx, creds, instID, ordID)
	}
	body := cancelOrderReq{InstID: instID, OrdID: ordID}
	var resp placeOrderResp
	if err := c.doRequest(ctx, creds, http.MethodPost, "/api/v5/trade/cancel-order", body, &resp); err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	if resp.Code != "0" {
		// 51400 = order already cancelled or filled — idempotent success.
		if resp.Code == "51400" {
			return nil
		}
		return fmt.Errorf("okx cancel: code=%s msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// cancelAlgoOrder cancels an algo (OCO/conditional) order via /api/v5/trade/cancel-algos.
func (c *Client) cancelAlgoOrder(ctx context.Context, creds exchange.Credentials, instID, algoID string) error {
	body := []map[string]string{{"algoId": algoID, "instId": instID}}
	var resp struct {
		okxEnvelope
		Data []struct {
			AlgoId string `json:"algoId"`
			SCode  string `json:"sCode"`
			SMsg   string `json:"sMsg"`
		} `json:"data"`
	}
	if err := c.doRequest(ctx, creds, http.MethodPost, "/api/v5/trade/cancel-algos", body, &resp); err != nil {
		return fmt.Errorf("cancel algo order: %w", err)
	}
	if resp.Code != "0" {
		return fmt.Errorf("okx cancel algo: code=%s msg=%s", resp.Code, resp.Msg)
	}
	if len(resp.Data) > 0 && resp.Data[0].SCode != "" && resp.Data[0].SCode != "0" {
		// 51298 = algo order does not exist (already triggered/cancelled).
		if resp.Data[0].SCode == "51298" {
			return nil
		}
		return fmt.Errorf("okx cancel algo: sCode=%s sMsg=%s", resp.Data[0].SCode, resp.Data[0].SMsg)
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

// ListLiveAlgoOrders implements exchange.AlgoOrderLister.
// Returns active OCO/conditional algo orders for the given instrument.
// Used during startup recovery to find existing brackets before re-placing.
func (c *Client) ListLiveAlgoOrders(ctx context.Context, creds exchange.Credentials, instID string) ([]exchange.LiveAlgoOrder, error) {
	path := fmt.Sprintf("/api/v5/trade/orders-algo?instId=%s&ordType=oco,conditional&state=live", instID)
	var resp struct {
		okxEnvelope
		Data []struct {
			AlgoId      string `json:"algoId"`
			InstId      string `json:"instId"`
			OrdType     string `json:"ordType"`
			SlTriggerPx string `json:"slTriggerPx"`
			TpTriggerPx string `json:"tpTriggerPx"`
		} `json:"data"`
	}
	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("list live algo orders: %w", err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx list algo orders: code=%s msg=%s", resp.Code, resp.Msg)
	}
	result := make([]exchange.LiveAlgoOrder, 0, len(resp.Data))
	for _, d := range resp.Data {
		result = append(result, exchange.LiveAlgoOrder{
			AlgoID:     d.InstId + ":A:" + d.AlgoId,
			InstID:     d.InstId,
			OrdType:    d.OrdType,
			StopLoss:   parseDecimal(d.SlTriggerPx),
			TakeProfit: parseDecimal(d.TpTriggerPx),
		})
	}
	return result, nil
}

// AmendOrder modifies price or qty of an existing live order.
func (c *Client) AmendOrder(ctx context.Context, creds exchange.Credentials, instID, orderID, newSz, newPx string) error {
	_, ordID, _ := parseOKXOrderID(orderID) // strip "instID:" prefix if present
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
// The returned order IDs use the "INSTID:A:algoId" format to distinguish algo
// orders from regular orders throughout the runtime (GetOrder, CancelOrder, WS routing).
func (c *Client) PlaceExitOrders(ctx context.Context, creds exchange.Credentials, req exchange.ExitOrderRequest) (*exchange.ExitOrderResult, error) {
	side := "sell"
	if req.Side == exchange.Buy {
		side = "buy"
	}
	tdMode := "cash"
	if req.Market == exchange.MarketFutures {
		tdMode = "cross"
		if req.MarginMode == "isolated" {
			tdMode = "isolated"
		}
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
	if d := resp.Data[0]; d.SCode != "" && d.SCode != "0" {
		return nil, fmt.Errorf("okx algo order rejected: sCode=%s sMsg=%s", d.SCode, d.SMsg)
	}
	// Use ":A:" infix to mark this as an algo order ID throughout the runtime.
	return &exchange.ExitOrderResult{
		OrderIDs: []string{req.Symbol + ":A:" + resp.Data[0].AlgoID},
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
