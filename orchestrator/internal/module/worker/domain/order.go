package domain

import "time"

// Order represents an order tracked by the bot.
type Order struct {
	BotID          string    `json:"bot_id"`
	OrchestratorID string    `json:"orchestrator_id"`
	ID             string    `json:"id"`
	Symbol         string    `json:"symbol"`
	Side           string    `json:"side"`
	Qty            float64   `json:"qty"`
	Type           string    `json:"type"`
	Status         string    `json:"status"`
	FilledQty      float64   `json:"filled_qty"`
	FilledAvg      float64   `json:"filled_avg_price"`
	SubmitTime     time.Time `json:"submitted_at"`
}
