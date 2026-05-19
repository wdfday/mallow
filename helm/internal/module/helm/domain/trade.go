package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// TradeReply is the orchestrator runtime's decision on a trade proposal.
type TradeReply struct {
	Approved     bool            `json:"approved"`
	Qty          decimal.Decimal `json:"qty"`           // base qty; zero when QuoteQty is set
	QuoteQty     decimal.Decimal `json:"quote_qty"`     // quote qty (e.g. 1000 USDT); mutually exclusive with Qty
	Side         string          `json:"side"`          // "buy" | "sell"
	EntryType    string          `json:"entry_type"`    // "market" | "limit" | "twap"
	TIF          string          `json:"tif,omitempty"` // time-in-force: "" | "gtc" | "ioc" | "fok" | "day"
	LimitPrice   decimal.Decimal `json:"limit_price,omitempty"`
	StopLoss     decimal.Decimal `json:"stop_loss,omitempty"`
	TakeProfit   decimal.Decimal `json:"take_profit,omitempty"`
	TrailingStop decimal.Decimal `json:"trailing_stop,omitempty"` // trailing stop as fraction; 0 = fixed stop
	Reason       string          `json:"reason,omitempty"`        // populated when rejected
}

// FillReport is a bot's fill notification to the orchestrator.
type FillReport struct {
	HandID    string          `json:"hand_id"`
	HelmID    string          `json:"helm_id"`
	OrderID   string          `json:"order_id"`
	Symbol    string          `json:"symbol"`
	Side      string          `json:"side"` // "buy" | "sell"
	Qty       decimal.Decimal `json:"qty"`
	Price     decimal.Decimal `json:"price"`
	Timestamp time.Time       `json:"timestamp"`
}
