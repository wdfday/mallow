package domain

// AccountType identifies the trading account segment.
// Mirrors investment's account_type enum so no information is lost crossing the NATS boundary.
type AccountType string

const (
	AccountTypeSpot         AccountType = "spot"          // spot / equities (Alpaca, Binance spot)
	AccountTypeFuturesUSDM  AccountType = "futures_usdm"  // USDT-margined perp/futures (Binance /fapi/)
	AccountTypeFuturesCOINM AccountType = "futures_coinm" // coin-margined futures (Binance /dapi/)
	AccountTypeUnified      AccountType = "unified"       // unified margin pool (OKX UTA, Bybit UTA)
	AccountTypeOptions      AccountType = "options"       // options-only account
)

// ExchangeConfig holds broker connection details used transiently when spawning a runtime.
// Never persisted to the helms table — always fetched from investment service at spawn time.
type ExchangeConfig struct {
	BrokerType  string      `json:"broker_type"`            // alpaca | binance | okx | bybit | ibkr | oanda
	AccountType AccountType `json:"account_type,omitempty"` // spot | futures | unified
	APIKey      string      `json:"api_key,omitempty"`
	APISecret   string      `json:"api_secret,omitempty"`
	Passphrase  string      `json:"passphrase,omitempty"` // OKX
	AccountID   string      `json:"account_id,omitempty"` // IBKR / Oanda
	BaseURL     string      `json:"base_url,omitempty"`
	StreamURL   string      `json:"stream_url,omitempty"` // Oanda
	Paper       bool        `json:"paper,omitempty"`      // true = paper/demo trading (no real money)
}

// RiskConfig holds account-level circuit-breakers / guards — no sizing. Hands own their
// own capital (AllocatedCapital) and size within it; per-hand edge-degradation guards live
// in hand.HandGuardConfig. There is no account-level %-per-position cap or cash reserve:
// reserve is implicit (TotalEquity − Σ AllocatedCapital), per-position size is the hand's.
type RiskConfig struct {
	// MaxPositions caps the number of concurrent open position units across all hands
	// (account-wide breadth). Enforced only when entering a NEW symbol — pyramiding into an
	// existing position is exempt; per-hand pyramid depth is capped by PositionConfig.MaxUnits.
	// Zero = no breadth cap.
	MaxPositions int `json:"max_positions,omitempty"`

	// DailyLossLimitPct halts trading for the rest of the day when daily PnL loss
	// exceeds this fraction of equity (e.g. 0.02 = 2%). 0 = disabled.
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct,omitempty"`

	// MaxDrawdownPct permanently halts all trading when the portfolio drawdown from
	// peak equity exceeds this fraction (e.g. 0.10 = 10%). 0 = disabled.
	MaxDrawdownPct float64 `json:"max_drawdown_pct,omitempty"`

	// MaxGrossExposurePct caps total open notional (Σ|qty|×price) as a fraction of equity.
	// Binds pyramid adds (unlike MaxPositions) — the account-blowup ceiling under aggressive
	// stacking. 1.0 = no leverage (gross ≤ equity); >1 permits leverage; 0 = disabled.
	MaxGrossExposurePct float64 `json:"max_gross_exposure_pct,omitempty"`
}

// Defaults is intentionally permissive: every guard is OPT-IN, where 0 = disabled
// (enforced by risk.Manager's gates). We ship NO tight default — a default like 2%/day
// or 10% drawdown only fires during normal testing and forces users to go edit it, which
// is worse UX than leaving the account unguarded until the user deliberately sets a limit.
func (r *RiskConfig) Defaults() {}
