package dto

import (
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// ── Exchange metrics DTOs ──────────────────────────────────────────────────

// ExchangeActionMetricsResp holds latency/error stats for one HTTP action type.
type ExchangeActionMetricsResp struct {
	Calls     int64   `json:"calls"`
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"error_rate"`
	AvgMs     float64 `json:"avg_ms"`
	MinMs     float64 `json:"min_ms"`
	MaxMs     float64 `json:"max_ms"`
}

// ExchangeWSMetricsResp holds real-time WS stream health stats.
type ExchangeWSMetricsResp struct {
	StreamStarts  int64   `json:"stream_starts"`
	StreamErrors  int64   `json:"stream_errors"`
	OrderEvents   int64   `json:"order_events"`
	BalanceEvents int64   `json:"balance_events"`
	IdleSec       float64 `json:"idle_sec"` // seconds since last WS event; 0 = never received
}

// ExchangeMetricsResp is the full exchange instrumentation snapshot.
type ExchangeMetricsResp struct {
	Exchange    string                    `json:"exchange"`
	PlaceOrder  ExchangeActionMetricsResp `json:"place_order"`
	GetOrder    ExchangeActionMetricsResp `json:"get_order"`
	CancelOrder ExchangeActionMetricsResp `json:"cancel_order"`
	SyncAccount ExchangeActionMetricsResp `json:"sync_account"`
	PingLastMs  float64                   `json:"ping_last_ms"`
	WS          ExchangeWSMetricsResp     `json:"ws"`
}

// ExchangePingResp is returned by the ping endpoint.
type ExchangePingResp struct {
	Exchange  string  `json:"exchange"`
	LatencyMs float64 `json:"latency_ms"`  // HTTP round-trip measured now
	WSIdleSec float64 `json:"ws_idle_sec"` // seconds since last WS event
}

func mapActionSnapshot(s exchange.ActionSnapshot) ExchangeActionMetricsResp {
	return ExchangeActionMetricsResp{
		Calls:     s.Calls,
		Errors:    s.Errors,
		ErrorRate: s.ErrorRate,
		AvgMs:     s.AvgMs,
		MinMs:     s.MinMs,
		MaxMs:     s.MaxMs,
	}
}

// MetricsSnapshotToResp converts an exchange.MetricsSnapshot to its DTO form.
func MetricsSnapshotToResp(s exchange.MetricsSnapshot) ExchangeMetricsResp {
	return ExchangeMetricsResp{
		Exchange:    s.Name,
		PlaceOrder:  mapActionSnapshot(s.PlaceOrder),
		GetOrder:    mapActionSnapshot(s.GetOrder),
		CancelOrder: mapActionSnapshot(s.CancelOrder),
		SyncAccount: mapActionSnapshot(s.SyncAccount),
		PingLastMs:  s.PingLastMs,
		WS: ExchangeWSMetricsResp{
			StreamStarts:  s.WS.StreamStarts,
			StreamErrors:  s.WS.StreamErrors,
			OrderEvents:   s.WS.OrderEvents,
			BalanceEvents: s.WS.BalanceEvents,
			IdleSec:       s.WS.IdleSec,
		},
	}
}

// ── Exchange probe DTOs ────────────────────────────────────────────────────

type AssetBalanceResp struct {
	Asset string          `json:"asset"`
	Free  decimal.Decimal `json:"free"`
}

type ExchangeAccountResp struct {
	Cash      decimal.Decimal        `json:"cash"`
	Equity    decimal.Decimal        `json:"equity"`
	Positions []ExchangePositionResp `json:"positions"`
	Balances  []AssetBalanceResp     `json:"balances"`
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
