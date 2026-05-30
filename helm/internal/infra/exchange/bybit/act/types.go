package act

// types.go — Bybit V5 native request/response types and converters to internal exchange types.
//
// Layout:
//   Parse helpers      — string → decimal / float64 / int64
//   Common envelope    — apiResponse[T] generic wrapper
//   Order types        — place, cancel, get order request/response structs
//   Account types      — wallet balance, fee rate structs
//   Position types     — position list response structs
//   Futures types      — funding rate, ticker structs
//   History types      — execution list response structs
//   WS types           — private WebSocket order event struct
//   Converters         — Bybit native → exchange.OrderResult / exchange.OrderSide / status string

import (
	"strconv"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// ── Parse helpers ─────────────────────────────────────────────────────────────

func parseDecimal(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseTimestampMs(s string) int64 {
	ms, _ := strconv.ParseInt(s, 10, 64)
	return ms
}

// ── Common envelope ───────────────────────────────────────────────────────────

// apiResponse is the generic Bybit V5 API response envelope.
type apiResponse[T any] struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  T      `json:"result"`
}

// ── Order request types ───────────────────────────────────────────────────────

// createOrderReq is the body for POST /v5/order/create.
type createOrderReq struct {
	Category      string `json:"category"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`      // Buy | Sell
	OrderType     string `json:"orderType"` // Market | Limit
	Qty           string `json:"qty"`
	OrderLinkId   string `json:"orderLinkId,omitempty"` // caller client order id; echoed on WS + queries
	Price         string `json:"price,omitempty"`
	TimeInForce   string `json:"timeInForce"`
	ReduceOnly    bool   `json:"reduceOnly,omitempty"`
	StopOrderType string `json:"stopOrderType,omitempty"` // Stop | TakeProfit
	TriggerPrice  string `json:"triggerPrice,omitempty"`  // conditional trigger
	TriggerBy     string `json:"triggerBy,omitempty"`     // LastPrice | MarkPrice
	// Bybit V5 spot: market Buy defaults qty to quote (USDT); "baseCoin" makes qty base (BTC).
	MarketUnit string `json:"marketUnit,omitempty"`
}

// cancelOrderReq is the body for POST /v5/order/cancel.
type cancelOrderReq struct {
	Category string `json:"category"`
	Symbol   string `json:"symbol,omitempty"`
	OrderID  string `json:"orderId"`
}

// ── Order response types ──────────────────────────────────────────────────────

// createOrderResult is the result field for POST /v5/order/create.
type createOrderResult struct {
	OrderID     string `json:"orderId"`
	OrderLinkId string `json:"orderLinkId"`
}

// orderListResult is the result field for GET /v5/order/realtime and /v5/order/history.
type orderListResult struct {
	List []orderDetail `json:"list"`
}

// orderDetail is a single order record returned by the Bybit order endpoints.
type orderDetail struct {
	OrderID     string `json:"orderId"`
	OrderLinkId string `json:"orderLinkId"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`        // "Buy" | "Sell"
	OrderStatus string `json:"orderStatus"` // "New" | "PartiallyFilled" | "Filled" | "Cancelled"
	CumExecQty  string `json:"cumExecQty"`  // cumulative filled size
	AvgPrice    string `json:"avgPrice"`    // average fill price
	Qty         string `json:"qty"`         // original order size
	Price       string `json:"price"`       // limit price
}

// ── Account types ─────────────────────────────────────────────────────────────

// walletBalanceResult is the result field for GET /v5/account/wallet-balance.
type walletBalanceResult struct {
	List []walletAccount `json:"list"`
}

type walletAccount struct {
	AccountType        string       `json:"accountType"`
	TotalEquity        string       `json:"totalEquity"`
	TotalWalletBalance string       `json:"totalWalletBalance"`
	TotalPnl           string       `json:"totalPnl"`
	Coin               []coinDetail `json:"coin"`
}

type coinDetail struct {
	Coin                string `json:"coin"`
	WalletBalance       string `json:"walletBalance"`
	AvailableToWithdraw string `json:"availableToWithdraw"`
	AvailableBalance    string `json:"availableBalance"` // UNIFIED: available for trading
	Locked              string `json:"locked"`
	UnrealizedPnl       string `json:"unrealisedPnl"`
	CumRealisedPnl      string `json:"cumRealisedPnl"`
}

// feeRateResult is the result field for GET /v5/account/fee-rate.
type feeRateResult struct {
	List []feeRateDetail `json:"list"`
}

type feeRateDetail struct {
	Symbol       string `json:"symbol"`
	MakerFeeRate string `json:"makerFeeRate"`
	TakerFeeRate string `json:"takerFeeRate"`
}

// ── Position types ────────────────────────────────────────────────────────────

// positionListResult is the result field for GET /v5/position/list.
type positionListResult struct {
	List []positionDetail `json:"list"`
}

type positionDetail struct {
	Symbol        string `json:"symbol"`
	Side          string `json:"side"` // "Buy" | "Sell" | "None"
	Size          string `json:"size"` // absolute position size
	AvgPrice      string `json:"avgPrice"`
	UnrealisedPnl string `json:"unrealisedPnl"`
}

// ── Futures types ─────────────────────────────────────────────────────────────

// fundingHistoryResult is the result field for GET /v5/market/funding/history.
type fundingHistoryResult struct {
	List []struct {
		Symbol      string `json:"symbol"`
		FundingRate string `json:"fundingRate"`
	} `json:"list"`
}

// tickerResult is the result field for GET /v5/market/tickers.
type tickerResult struct {
	List []struct {
		Symbol    string `json:"symbol"`
		MarkPrice string `json:"markPrice"`
		LastPrice string `json:"lastPrice"`
	} `json:"list"`
}

// ── History types ─────────────────────────────────────────────────────────────

// execListResult is the result field for GET /v5/execution/list.
type execListResult struct {
	List []bybitExecution `json:"list"`
}

// bybitExecution is a single execution record from the execution/list endpoint.
type bybitExecution struct {
	OrderID     string `json:"orderId"`
	OrderLinkId string `json:"orderLinkId"` // caller client order id, echoed by execution/list
	ExecID      string `json:"execId"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"` // "Buy" | "Sell"
	ExecQty     string `json:"execQty"`
	ExecPrice   string `json:"execPrice"`
	ExecFee     string `json:"execFee"`
	FeeCurrency string `json:"feeCurrency"` // fee asset; e.g. "BTC", "USDT"
	ExecTime    string `json:"execTime"`    // Unix ms string
}

// ── WebSocket types ───────────────────────────────────────────────────────────

// bybitOrderEvent is the private WebSocket "order" topic message.
type bybitOrderEvent struct {
	Topic string `json:"topic"`
	Data  []struct {
		OrderID     string `json:"orderId"`
		OrderLinkId string `json:"orderLinkId"`
		Symbol      string `json:"symbol"`
		Side        string `json:"side"`        // "Buy" | "Sell"
		OrderStatus string `json:"orderStatus"` // "New" | "PartiallyFilled" | "Filled" | "Cancelled" | "Rejected"
		ExecType    string `json:"execType"`    // "New" (ack) | "Trade" (fill)
		ExecId      string `json:"execId"`      // exchange fill ID — unique per partial fill
		Qty         string `json:"qty"`
		ExecQty     string `json:"execQty"`
		ExecPrice   string `json:"execPrice"`
		ExecFee     string `json:"execFee"`     // fee for this fill leg
		FeeCurrency string `json:"feeCurrency"` // fee asset; e.g. "BTC", "USDT"
		UpdatedTime string `json:"updatedTime"` // Unix ms string
	} `json:"data"`
}

// ── Converters: Bybit native → internal ──────────────────────────────────────

// orderDetailToResult converts a Bybit orderDetail record to exchange.OrderResult.
func orderDetailToResult(o *orderDetail) *exchange.OrderResult {
	return &exchange.OrderResult{
		ID:            o.OrderID,
		ClientOrderID: o.OrderLinkId,
		Symbol:        o.Symbol,
		Side:          exchange.OrderSide(mapSide(o.Side)),
		Status:        mapOrderStatus(o.OrderStatus),
		FilledQty:     parseDecimal(o.CumExecQty),
		FilledAvg:     parseDecimal(o.AvgPrice),
		Qty:           parseDecimal(o.Qty),
	}
}

// mapSide converts Bybit's "Buy"/"Sell" (title-case) to internal exchange.OrderSide.
func mapSide(s string) exchange.OrderSide {
	if s == "Sell" {
		return exchange.Sell
	}
	return exchange.Buy
}

// toBybitSide converts internal exchange.OrderSide to Bybit's title-case "Buy"/"Sell".
func toBybitSide(s exchange.OrderSide) string {
	if s == exchange.Sell {
		return "Sell"
	}
	return "Buy"
}

// mapOrderStatus translates Bybit order status strings to canonical internal status strings.
func mapOrderStatus(s string) string {
	switch s {
	case "New":
		return "new"
	case "PartiallyFilled":
		return "partially_filled"
	case "Filled":
		return "filled"
	case "Cancelled":
		return "canceled"
	default:
		return s
	}
}
