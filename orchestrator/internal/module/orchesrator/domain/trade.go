package domain

import "time"

// TradeReply is the orchestrator runtime's decision on a trade proposal.
type TradeReply struct {
	Approved     bool    `json:"approved"`
	Qty          float64 `json:"qty"`
	Side         string  `json:"side"`       // "buy" | "sell"
	EntryType    string  `json:"entry_type"` // "market" | "limit" | "twap"
	LimitPrice   float64 `json:"limit_price,omitempty"`
	StopLoss     float64 `json:"stop_loss,omitempty"`
	TakeProfit   float64 `json:"take_profit,omitempty"`
	TrailingStop float64 `json:"trailing_stop,omitempty"` // trailing stop as fraction; 0 = fixed stop
	Reason       string  `json:"reason,omitempty"`        // populated when rejected
}

// FillReport is a bot's fill notification to the orchestrator.
type FillReport struct {
	BotID     string    `json:"bot_id"`
	AccountID string    `json:"account_id"`
	OrderID   string    `json:"order_id"`
	Symbol    string    `json:"symbol"`
	Side      string    `json:"side"` // "buy" | "sell"
	Qty       float64   `json:"qty"`
	Price     float64   `json:"price"`
	Timestamp time.Time `json:"timestamp"`
}
