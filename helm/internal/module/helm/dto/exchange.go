package dto

import (
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// ── Exchange probe DTOs ────────────────────────────────────────────────────

type ExchangeAccountResp struct {
	Cash      decimal.Decimal        `json:"cash"`
	Equity    decimal.Decimal        `json:"equity"`
	Positions []ExchangePositionResp `json:"positions"`
}

type ExchangePositionResp struct {
	Symbol   string          `json:"symbol"`
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
	CurPrice decimal.Decimal `json:"cur_price"`
}

type ExchangePriceResp struct {
	Symbol string          `json:"symbol"`
	Price  decimal.Decimal `json:"price"`
}

type PlaceExchangeOrderReq struct {
	Symbol string  `json:"symbol" binding:"required"`
	Side   string  `json:"side"   binding:"required,oneof=buy sell"`
	Type   string  `json:"type"   binding:"required,oneof=market limit"`
	Qty    float64 `json:"qty"    binding:"required,gt=0"`
	Price  float64 `json:"price"`
}

type ExchangeOrderResp struct {
	ID        string          `json:"id"`
	Symbol    string          `json:"symbol"`
	Side      string          `json:"side"`
	Status    string          `json:"status"`
	Qty       decimal.Decimal `json:"qty"`
	FilledQty decimal.Decimal `json:"filled_qty"`
	FilledAvg decimal.Decimal `json:"filled_avg_price"`
}

func MapOrderResult(r *exchange.OrderResult) ExchangeOrderResp {
	return ExchangeOrderResp{
		ID:        r.ID,
		Symbol:    r.Symbol,
		Side:      string(r.Side),
		Status:    r.Status,
		Qty:       r.Qty,
		FilledQty: r.FilledQty,
		FilledAvg: r.FilledAvg,
	}
}
