package act

// types.go — OKX V5 native request/response types and converters to internal exchange types.
//
// Layout:
//   Parse helpers      — string → decimal / float64 / int64
//   Common envelope    — okxEnvelope (code + msg wrapper shared by all OKX responses)
//   Order types        — place, cancel, get, algo order request/response structs
//   Fill history types — fills-history response struct
//   Converters         — OKX native → exchange.OrderResult / exchange.OrderSide / status string

import (
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
)

// ── Parse helpers ─────────────────────────────────────────────────────────────

func parseDecimal(s string) decimal.Decimal {
	d, _ := decimal.NewFromString(s)
	return d
}

func parseFloat(s string) float64 {
	f, _ := fmt.Sscanf(s, "%f", new(float64))
	_ = f
	var out float64
	fmt.Sscanf(s, "%f", &out) //nolint:errcheck
	return out
}

func parseTimestampMs(s string) int64 {
	var ms int64
	fmt.Sscanf(s, "%d", &ms) //nolint:errcheck
	return ms
}

// ── Common envelope ───────────────────────────────────────────────────────────

// okxEnvelope is the outer wrapper for every OKX V5 API response.
type okxEnvelope struct {
	Code string `json:"code"` // "0" on success
	Msg  string `json:"msg"`
}

// okxRejectedError carries a per-order error code from OKX (sCode / sMsg fields).
// Returned by PlaceOrder when outer code == "0" but per-order sCode != "0",
// allowing MeteredExchange.classifyErr to use errors.As for precise classification.
type okxRejectedError struct {
	SCode string
	SMsg  string
}

func (e *okxRejectedError) Error() string {
	return fmt.Sprintf("okx order rejected: sCode=%s sMsg=%s", e.SCode, e.SMsg)
}

// ClassifyError implements exchange.ErrorClassifier using OKX per-order sCodes.
func (c *Client) ClassifyError(err error) exchange.ErrClass {
	var oe *okxRejectedError
	if !errors.As(err, &oe) {
		return exchange.ClassifyGeneric(err)
	}
	return classifyOKXSCode(oe.SCode)
}

// classifyOKXSCode maps OKX V5 per-order sCodes to an ErrClass.
// Reference: https://www.okx.com/docs-v5/en/#error-code
func classifyOKXSCode(sCode string) exchange.ErrClass {
	switch sCode {
	case "50111", "50113", "50119": // invalid API key, invalid sign, invalid broker
		return exchange.ErrClassAuth
	case "50014", "50018": // rate limit exceeded, account suspended
		return exchange.ErrClassRateLimit
	case "51006", "51008", "51100", "51131", "51300": // insufficient balance / margin
		return exchange.ErrClassInsufficientBalance
	case "51001": // instrument ID does not exist
		return exchange.ErrClassInvalidSymbol
	case "51003", "51121", "51203": // order qty below min / out of range
		return exchange.ErrClassLotSize
	case "51010": // order does not exist
		return exchange.ErrClassOrderNotFound
	case "50000", "50001", "50002": // body empty / server unavailable / overloaded
		return exchange.ErrClassServerError
	default:
		return exchange.ErrClassUnknown
	}
}

// ── Order request types ───────────────────────────────────────────────────────

// placeOrderReq is the body for POST /api/v5/trade/order.
type placeOrderReq struct {
	InstID     string `json:"instId"`
	TdMode     string `json:"tdMode"`            // cash | cross | isolated
	Side       string `json:"side"`              // buy | sell
	OrdType    string `json:"ordType"`           // market | limit | post_only | fok | ioc
	Sz         string `json:"sz"`                // order size in base currency (or quote for market buy)
	ClOrdID    string `json:"clOrdId,omitempty"` // caller client order id (clOrdId); echoed back on WS + queries
	Px         string `json:"px,omitempty"`
	TgtCcy     string `json:"tgtCcy,omitempty"`     // base_ccy (spot market buy in base qty)
	ReduceOnly bool   `json:"reduceOnly,omitempty"` // futures only
}

// cancelOrderReq is the body for POST /api/v5/trade/cancel-order.
type cancelOrderReq struct {
	InstID string `json:"instId,omitempty"`
	OrdID  string `json:"ordId"`
}

// algoOrderReq is the body for POST /api/v5/trade/order-algo.
// Supports ordType: oco | conditional | move_order_stop | iceberg | twap.
type algoOrderReq struct {
	InstID      string `json:"instId"`
	TdMode      string `json:"tdMode"`
	Side        string `json:"side"`
	OrdType     string `json:"ordType"` // oco | conditional
	Sz          string `json:"sz"`
	ReduceOnly  bool   `json:"reduceOnly,omitempty"`
	TpTriggerPx string `json:"tpTriggerPx,omitempty"` // take-profit trigger price
	TpOrdPx     string `json:"tpOrdPx,omitempty"`     // "-1" = market execution
	SlTriggerPx string `json:"slTriggerPx,omitempty"` // stop-loss trigger price
	SlOrdPx     string `json:"slOrdPx,omitempty"`     // "-1" = market execution
}

// ── Order response types ──────────────────────────────────────────────────────

// placeOrderResp is the response for POST /api/v5/trade/order.
type placeOrderResp struct {
	okxEnvelope
	Data []struct {
		OrdID   string `json:"ordId"`
		ClOrdID string `json:"clOrdId"`
		SCode   string `json:"sCode"` // per-order error code; "0" = success
		SMsg    string `json:"sMsg"`
	} `json:"data"`
}

// algoOrderResp is the response for POST /api/v5/trade/order-algo.
type algoOrderResp struct {
	okxEnvelope
	Data []struct {
		AlgoID string `json:"algoId"`
		SCode  string `json:"sCode"`
		SMsg   string `json:"sMsg"`
	} `json:"data"`
}

// getOrderResp is the response for GET /api/v5/trade/order and
// GET /api/v5/trade/orders-pending.
type getOrderResp struct {
	okxEnvelope
	Data []orderData `json:"data"`
}

// orderData is a single order record returned by the OKX order endpoints.
type orderData struct {
	OrdID     string `json:"ordId"`
	ClOrdID   string `json:"clOrdId"`
	InstID    string `json:"instId"`
	Side      string `json:"side"`      // "buy" | "sell"
	State     string `json:"state"`     // "live" | "partially_filled" | "filled" | "canceled"
	AccFillSz string `json:"accFillSz"` // cumulative filled size (gross, before fee)
	AvgPx     string `json:"avgPx"`     // average fill price
	Sz        string `json:"sz"`        // original order size
	Px        string `json:"px"`        // limit price (empty for market orders)
	// Fee fields — required for normalizeCommission in the REST poll path.
	// When feeCcy == base asset (e.g. "SOL" for SOL-USDT), the fee is deducted
	// from accFillSz to get the net quantity actually held. Without this,
	// the OCO bracket is sized for gross qty > net qty held → OKX rejects with
	// "insufficient balance / margin too low for borrowing".
	Fee    string `json:"fee"`    // cumulative fee (negative = cost, e.g. "-0.003282966")
	FeeCcy string `json:"feeCcy"` // fee currency (e.g. "SOL", "USDT")
}

// ── Fill history types ────────────────────────────────────────────────────────

// fillsHistoryResp is the response for GET /api/v5/trade/fills-history.
type fillsHistoryResp struct {
	okxEnvelope
	Data []okxFill `json:"data"`
}

// okxFill is a single fill record from the fills-history endpoint.
type okxFill struct {
	InstID  string `json:"instId"` // e.g. "BTC-USDT"
	TradeID string `json:"tradeId"`
	OrdID   string `json:"ordId"`
	ClOrdID string `json:"clOrdId"` // caller client order id, echoed by fills-history
	BillID  string `json:"billId"`  // cursor for pagination (after= parameter)
	Side    string `json:"side"`    // "buy" | "sell"
	FillSz  string `json:"fillSz"`
	FillPx  string `json:"fillPx"`
	Fee     string `json:"fee"`    // negative value (cost); negate to get absolute fee
	FeeCcy  string `json:"feeCcy"` // fee currency; e.g. "BTC", "USDT"
	TS      string `json:"ts"`     // Unix millisecond timestamp as string
}

// ── Converters: OKX native → internal ────────────────────────────────────────

// orderDataToResult converts an OKX orderData record to exchange.OrderResult.
// Commission is set from the cumulative fee field so that normalizeCommission
// in the REST poll path can correctly deduct base-asset fees from accFillSz.
func orderDataToResult(d *orderData) *exchange.OrderResult {
	fee := parseDecimal(d.Fee)
	if fee.IsNegative() {
		fee = fee.Neg() // normalise to positive (OKX sends as negative cost)
	}
	return &exchange.OrderResult{
		ID:              d.InstID + ":" + d.OrdID,
		ClientOrderID:   d.ClOrdID,
		Symbol:          d.InstID,
		Side:            exchange.OrderSide(d.Side),
		Status:          mapOrderStatus(d.State),
		Qty:             parseDecimal(d.Sz),
		FilledQty:       parseDecimal(d.AccFillSz),
		FilledAvg:       parseDecimal(d.AvgPx),
		Commission:      fee,
		CommissionAsset: d.FeeCcy,
	}
}

// mapOrderStatus translates OKX order state strings to canonical internal status strings.
func mapOrderStatus(state string) string {
	switch state {
	case "live":
		return "new"
	case "partially_filled":
		return "partially_filled"
	case "filled":
		return "filled"
	case "canceled", "mmp_canceled":
		return "canceled"
	default:
		return state
	}
}

// parseOKXOrderID splits the composite order key used by PlaceOrder and PlaceExitOrders.
//
//	Regular order:  "BTC-USDT:12345"      → instID="BTC-USDT" ordID="12345"  isAlgo=false
//	Algo order:     "BTC-USDT:A:12345"    → instID="BTC-USDT" ordID="12345"  isAlgo=true
//
// The ":A:" infix marks algo orders (OCO/conditional placed via /api/v5/trade/order-algo).
// Returns ("_", orderID, false) when the separator is absent to allow graceful fallback.
func parseOKXOrderID(orderID string) (instID, ordID string, isAlgo bool) {
	if i := strings.Index(orderID, ":A:"); i >= 0 {
		return orderID[:i], orderID[i+3:], true
	}
	for i := len(orderID) - 1; i >= 0; i-- {
		if orderID[i] == ':' {
			return orderID[:i], orderID[i+1:], false
		}
	}
	return "_", orderID, false
}
