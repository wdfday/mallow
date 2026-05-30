package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Order represents an order tracked by the bot.
type Order struct {
	HandId string `json:"hand_id"`
	HelmId string `json:"helm_id"`
	ID     string `json:"id"`
	// ClientOrderID is the mallow-generated clOrdId for this order (empty for bracket
	// orders and the fallback-market path). Used to clean up clid-keyed routing entries
	// when the order reaches a terminal state. See CLIENT_ORDER_ID.md.
	ClientOrderID string          `json:"client_order_id,omitempty"`
	Symbol        string          `json:"symbol"`
	Side       string          `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	FilledQty  decimal.Decimal `json:"filled_qty"`
	FilledAvg  decimal.Decimal `json:"filled_avg_price"`
	SubmitTime time.Time       `json:"submitted_at"`
}
