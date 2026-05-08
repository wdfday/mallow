package runtime

import (
	"time"

	"github.com/shopspring/decimal"
)

// Signal is the input pushed into a Bot's signal channel.
type Signal struct {
	Symbol     string
	Direction  string
	Strength   float64
	ATR        float64   // optional: average true range for stop/TP sizing; converted to decimal at use site
	ReceivedAt time.Time // set by dispatcher; used for staleness check

	// Optional exit levels from the strategy.
	// When IsOffset=false (default): TargetPrice and StopPrice are absolute price levels.
	// When IsOffset=true: they are deltas from fill price
	//   (e.g. TargetPrice=+3.0, StopPrice=-1.0 for a long entry at 72.5 → TP=75.5, SL=71.5).
	// Zero value means not set — tactician config is used as fallback.
	TargetPrice decimal.Decimal
	StopPrice   decimal.Decimal
	IsOffset    bool
}

const signalMaxAge = 5 * time.Second

// IsUrgent reports whether the signal must not be dropped.
// close/exit signals are urgent — losing them means unclosed positions.
func (s Signal) IsUrgent() bool {
	return s.Direction == "close" || s.Direction == "exit_long" || s.Direction == "exit_short"
}

// HandHealth tracks liveness and error state.
type HandHealth struct {
	Status       string     `json:"status"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	LastSignalAt *time.Time `json:"last_signal_at,omitempty"`
	LastOrderAt  *time.Time `json:"last_order_at,omitempty"`
	LastErrorAt  *time.Time `json:"last_error_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	Uptime       string     `json:"uptime,omitempty"`
}

// HandMetrics tracks trading behavior counters.
type HandMetrics struct {
	SignalsReceived int64           `json:"signals_received"`
	SignalsFiltered int64           `json:"signals_filtered"`
	SignalsDropped  int64           `json:"signals_dropped"` // non-urgent signals lost due to full channel
	TradesApproved  int64           `json:"trades_approved"`
	OrdersPlaced    int64           `json:"orders_placed"`
	OrdersFilled    int64           `json:"orders_filled"`
	OrdersFailed    int64           `json:"orders_failed"`
	TotalPnL        decimal.Decimal `json:"total_pnl"`
	WinCount        int64           `json:"win_count"`
	LossCount       int64           `json:"loss_count"`
}
