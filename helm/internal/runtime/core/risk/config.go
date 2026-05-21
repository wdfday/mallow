package risk

// Config holds account-level risk management parameters.
// Applied by Manager on every trade intent before execution.
type Config struct {
	// MaxPositions is the maximum number of concurrent open positions.
	// New entry intents are rejected once this limit is reached.
	MaxPositions int `json:"max_positions"`

	// DailyLossLimitPct halts new entries for the rest of the calendar day
	// when realized daily PnL exceeds this fraction of equity (e.g. 0.02 = 2%).
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct"`

	// MaxDrawdownPct permanently halts all trading (until ResetHalt)
	// when portfolio drawdown from peak exceeds this fraction (e.g. 0.10 = 10%).
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`

	// MaxOrderRateLimit is the maximum number of approved entry/adjustments allowed per hand
	// within OrderRateWindowSec. If exceeded, trading is halted. 0 disables this gate.
	MaxOrderRateLimit int `json:"max_order_rate_limit"`

	// OrderRateWindowSec is the sliding window size in seconds for rate limit checks.
	OrderRateWindowSec int `json:"order_rate_window_sec"`
}

// DefaultConfig returns conservative defaults suitable for paper trading.
func DefaultConfig() Config {
	return Config{
		MaxPositions:       5,
		DailyLossLimitPct:  0.02,
		MaxDrawdownPct:     0.10,
		MaxOrderRateLimit:  15,
		OrderRateWindowSec: 60,
	}
}
