package oanda

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// PlaceOrder submits an order to OANDA.
// Symbols should be OANDA instrument format, e.g. "EUR_USD".
func (c *Client) PlaceOrder(ctx context.Context, creds exchange.Credentials, req exchange.OrderRequest) (*exchange.OrderResult, error) {
	units := req.Qty
	if req.Side == exchange.Sell {
		units = units.Neg()
	}

	var orderReq any
	if req.Type == exchange.Limit {
		orderReq = map[string]any{
			"order": map[string]any{
				"type":        "LIMIT",
				"instrument":  req.Symbol,
				"units":       units.String(),
				"price":       req.Price.String(),
				"timeInForce": "GTC",
			},
		}
	} else {
		orderReq = map[string]any{
			"order": map[string]any{
				"type":        "MARKET",
				"instrument":  req.Symbol,
				"units":       units.String(),
				"timeInForce": "FOK",
			},
		}
	}

	slog.Info("oanda: placing order", "instrument", req.Symbol, "side", req.Side, "qty", req.Qty)

	path := fmt.Sprintf("/v3/accounts/%s/orders", creds.AccountID)
	var resp orderCreateResponse
	if err := c.doRequest(ctx, creds, http.MethodPost, path, orderReq, &resp); err != nil {
		return nil, fmt.Errorf("place order: %w", err)
	}

	id := resp.OrderCreateTransaction.ID
	if id == "" {
		id = resp.OrderFillTransaction.ID
	}

	var filledAvg decimal.Decimal
	if resp.OrderFillTransaction.Price != "" {
		filledAvg, _ = decimal.NewFromString(resp.OrderFillTransaction.Price)
	}

	return &exchange.OrderResult{
		ID:        id,
		Symbol:    req.Symbol,
		Side:      req.Side,
		Status:    "filled",
		Qty:       req.Qty,
		FilledQty: req.Qty,
		FilledAvg: filledAvg,
	}, nil
}

// GetOrder retrieves order status.
func (c *Client) GetOrder(ctx context.Context, creds exchange.Credentials, orderID string) (*exchange.OrderResult, error) {
	path := fmt.Sprintf("/v3/accounts/%s/orders/%s", creds.AccountID, orderID)
	var resp orderGetResponse
	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("get order: %w", err)
	}

	o := resp.Order
	return &exchange.OrderResult{
		ID:     o.ID,
		Symbol: o.Instrument,
		Side:   mapSide(o.Units),
		Status: mapStatus(o.State),
	}, nil
}

// GetPendingOrders returns all pending orders for the given symbol (ignored by OANDA — returns all).
func (c *Client) GetPendingOrders(ctx context.Context, creds exchange.Credentials, _ string) ([]exchange.OrderResult, error) {
	path := fmt.Sprintf("/v3/accounts/%s/pendingOrders", creds.AccountID)
	var resp struct {
		Orders []struct {
			ID         string `json:"id"`
			Instrument string `json:"instrument"`
			Units      string `json:"units"`
			State      string `json:"state"`
			Type       string `json:"type"`
			Price      string `json:"price"`
		} `json:"orders"`
	}
	if err := c.doRequest(ctx, creds, http.MethodGet, path, nil, &resp); err != nil {
		return nil, fmt.Errorf("oanda pending orders: %w", err)
	}

	results := make([]exchange.OrderResult, len(resp.Orders))
	for i, o := range resp.Orders {
		results[i] = exchange.OrderResult{
			ID:     o.ID,
			Symbol: o.Instrument,
			Side:   mapSide(o.Units),
			Status: mapStatus(o.State),
		}
	}
	return results, nil
}

// CancelOrder cancels a pending order.
func (c *Client) CancelOrder(ctx context.Context, creds exchange.Credentials, orderID string) error {
	path := fmt.Sprintf("/v3/accounts/%s/orders/%s/cancel", creds.AccountID, orderID)
	var resp json.RawMessage
	if err := c.doRequest(ctx, creds, http.MethodPut, path, nil, &resp); err != nil {
		return fmt.Errorf("cancel order: %w", err)
	}
	return nil
}

func mapSide(units string) exchange.OrderSide {
	if strings.HasPrefix(units, "-") {
		return exchange.Sell
	}
	return exchange.Buy
}

func mapStatus(state string) string {
	switch state {
	case "PENDING":
		return "new"
	case "FILLED":
		return "filled"
	case "CANCELLED":
		return "canceled"
	default:
		return state
	}
}

// --- OANDA order types ---

type orderCreateResponse struct {
	OrderCreateTransaction struct {
		ID string `json:"id"`
	} `json:"orderCreateTransaction"`
	OrderFillTransaction struct {
		ID    string `json:"id"`
		Price string `json:"price"`
	} `json:"orderFillTransaction"`
}

type orderGetResponse struct {
	Order struct {
		ID         string `json:"id"`
		Instrument string `json:"instrument"`
		Units      string `json:"units"`
		State      string `json:"state"`
	} `json:"order"`
}
