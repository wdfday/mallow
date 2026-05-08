package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Order represents an order tracked by the bot.
type Order struct {
	HandId     string          `json:"hand_id"`
	HelmId     string          `json:"helm_id"`
	ID         string          `json:"id"`
	Symbol     string          `json:"symbol"`
	Side       string          `json:"side"`
	Qty        decimal.Decimal `json:"qty"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	FilledQty  decimal.Decimal `json:"filled_qty"`
	FilledAvg  decimal.Decimal `json:"filled_avg_price"`
	SubmitTime time.Time       `json:"submitted_at"`
}
