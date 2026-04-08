package orderbook

import "time"

// OrderSide represents the side of an order.
type OrderSide string

const (
	SideBuy  OrderSide = "buy"
	SideSell OrderSide = "sell"
)

// SymbolInfo holds exchange-level constraints for a trading pair.
type SymbolInfo struct {
	Symbol      string  `json:"symbol"`
	Active      bool    `json:"active"`
	MinQty      float64 `json:"min_qty"`
	MaxQty      float64 `json:"max_qty"`      // 0 = no limit
	StepSize    float64 `json:"step_size"`    // lot size increment
	MinNotional float64 `json:"min_notional"` // minimum order value in quote currency
	TickSize    float64 `json:"tick_size"`    // price increment
}

// ProposedOrder is the order a bot wants to place, before exchange validation.
type ProposedOrder struct {
	BotID     string    `json:"bot_id"`
	AccountID string    `json:"account_id"`
	Symbol    string    `json:"symbol"`
	Side      OrderSide `json:"side"`
	Qty       float64   `json:"qty"`
	Price     float64   `json:"price,omitempty"`
}

// ValidationResult is the outcome of orderbook validation.
type ValidationResult struct {
	Valid       bool    `json:"valid"`
	Reason      string  `json:"reason,omitempty"`
	AdjustedQty float64 `json:"adjusted_qty,omitempty"`
}

// PendingOrder is an order currently open on the exchange.
type PendingOrder struct {
	OrderID   string    `json:"order_id"`
	BotID     string    `json:"bot_id"`
	AccountID string    `json:"account_id"`
	Symbol    string    `json:"symbol"`
	Side      OrderSide `json:"side"`
	Qty       float64   `json:"qty"`
}

// BookUpdate is one market-data observation stored in the per-symbol history buffer.
// It is intentionally compact and value-based so callers receive immutable snapshots.
type BookUpdate struct {
	Symbol     string    `json:"symbol"`
	Sequence   uint64    `json:"sequence"`
	Bid        float64   `json:"bid,omitempty"`
	Ask        float64   `json:"ask,omitempty"`
	Last       float64   `json:"last,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
}
