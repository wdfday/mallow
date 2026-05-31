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

	// MaxGrossExposurePct caps total open notional (Σ|qty|×price) as a fraction of equity.
	// Unlike MaxPositions this DOES bind pyramid adds — it is the account-blowup ceiling that
	// lets a hand stack aggressively up to a known limit. Entries (incl. adds) are rejected
	// once current gross exposure ≥ this × equity. 0 disables the gate.
	// >1 permits leverage (futures); e.g. 3.0 = up to 3× equity gross.
	MaxGrossExposurePct float64 `json:"max_gross_exposure_pct"`

	// MaxOrderRateLimit is the maximum number of approved entry/adjustments allowed per hand
	// within OrderRateWindowSec. If exceeded, trading is halted. 0 disables this gate.
	MaxOrderRateLimit int `json:"max_order_rate_limit"`

	// OrderRateWindowSec is the sliding window size in seconds for rate limit checks.
	OrderRateWindowSec int `json:"order_rate_window_sec"`
}

// DefaultConfig returns fully permissive defaults: every guard disabled (0).
// Guards are opt-in — the user enables a limit deliberately. Shipping a tight default
// (e.g. 2%/day) only fires during normal testing and annoys users into editing it.
func DefaultConfig() Config {
	return Config{
		MaxPositions:        0, // unlimited
		DailyLossLimitPct:   0, // disabled
		MaxDrawdownPct:      0, // disabled
		MaxGrossExposurePct: 0, // disabled (no exposure ceiling)
		MaxOrderRateLimit:   0, // disabled
		OrderRateWindowSec:  0,
	}
}
