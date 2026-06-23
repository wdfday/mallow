package dto

import (
	"time"

	"mallow/helm/internal/infra/orderlog"
)

// OrderHistoryResp is the JSON shape for a persisted order lifecycle record.
type OrderHistoryResp struct {
	ExchangeOrderID string `json:"exchange_order_id"`
	ClientOrderID   string `json:"client_order_id,omitempty"`
	HandID          string `json:"hand_id,omitempty"`
	PositionID      string `json:"position_id,omitempty"`
	Symbol          string `json:"symbol"`
	Side            string `json:"side,omitempty"`
	OrderType       string `json:"order_type,omitempty"`
	Qty             string `json:"qty,omitempty"`
	Price           string `json:"price,omitempty"`
	Status          string `json:"status"`
	FilledQty       string `json:"filled_qty,omitempty"`
	FilledPrice     string `json:"filled_price,omitempty"`
	IsClose         bool   `json:"is_close"`
	Reason          string `json:"reason,omitempty"`
	PlacedAt        string `json:"placed_at,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

// OrderRecordToHistoryResp maps a persisted order record to its HTTP response shape.
func OrderRecordToHistoryResp(r orderlog.OrderRecord) OrderHistoryResp {
	resp := OrderHistoryResp{
		ExchangeOrderID: r.ExchangeOrderID,
		ClientOrderID:   r.ClientOrderID,
		HandID:          r.HandID,
		PositionID:      r.PositionID,
		Symbol:          r.Symbol,
		Side:            r.Side,
		OrderType:       r.OrderType,
		Status:          r.Status,
		IsClose:         r.IsClose,
		Reason:          r.Reason,
		UpdatedAt:       r.UpdatedAt.Format(time.RFC3339),
	}
	if r.Qty.IsPositive() {
		resp.Qty = r.Qty.String()
	}
	if r.Price.IsPositive() {
		resp.Price = r.Price.String()
	}
	if r.FilledQty.IsPositive() {
		resp.FilledQty = r.FilledQty.String()
	}
	if r.FilledPrice.IsPositive() {
		resp.FilledPrice = r.FilledPrice.String()
	}
	if !r.PlacedAt.IsZero() {
		resp.PlacedAt = r.PlacedAt.Format(time.RFC3339)
	}
	return resp
}
