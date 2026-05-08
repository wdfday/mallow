package orderbook

import (
	"time"

	"github.com/shopspring/decimal"
)

// OrderSide represents the side of an order.
type OrderSide string

const (
	SideBuy  OrderSide = "buy"
	SideSell OrderSide = "sell"
)

// SymbolInfo holds exchange-level constraints for a trading pair.
type SymbolInfo struct {
	Symbol      string          `json:"symbol"`
	Active      bool            `json:"active"`
	MinQty      decimal.Decimal `json:"min_qty"`
	MaxQty      decimal.Decimal `json:"max_qty"`      // zero = no limit
	StepSize    decimal.Decimal `json:"step_size"`    // lot size increment
	MinNotional decimal.Decimal `json:"min_notional"` // minimum order value in quote currency
	TickSize    decimal.Decimal `json:"tick_size"`    // price increment
}

// ProposedOrder is the order a hand wants to place, before exchange validation.
type ProposedOrder struct {
	BotID          string          `json:"hand_id"`
	OrchestratorID string          `json:"helm_id"`
	Symbol         string          `json:"symbol"`
	Side           OrderSide       `json:"side"`
	Qty            decimal.Decimal `json:"qty"`
	Price          decimal.Decimal `json:"price,omitempty"`
}

// ValidationResult is the outcome of orderbook validation.
type ValidationResult struct {
	Valid       bool            `json:"valid"`
	Reason      string          `json:"reason,omitempty"`
	AdjustedQty decimal.Decimal `json:"adjusted_qty,omitempty"`
}

// PendingOrder is an order currently open on the exchange.
type PendingOrder struct {
	OrderID        string          `json:"order_id"`
	BotID          string          `json:"hand_id"`
	OrchestratorID string          `json:"helm_id"`
	Symbol         string          `json:"symbol"`
	Side           OrderSide       `json:"side"`
	Qty            decimal.Decimal `json:"qty"`
}

// BookUpdate is one market-data observation stored in the per-symbol history buffer.
// It is intentionally compact and value-based so callers receive immutable snapshots.
type BookUpdate struct {
	Symbol     string          `json:"symbol"`
	Sequence   uint64          `json:"sequence"`
	Bid        decimal.Decimal `json:"bid,omitempty"`
	Ask        decimal.Decimal `json:"ask,omitempty"`
	Last       decimal.Decimal `json:"last,omitempty"`
	ReceivedAt time.Time       `json:"received_at"`
}
