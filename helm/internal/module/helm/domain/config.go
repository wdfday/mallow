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

// PortfolioConfig manages how the orchestrator allocates capital across bots.
// Think of it as the "sizing" layer at account level — analogous to bot.PositionConfig.
type PortfolioConfig struct {
	// MaxPositions is the account-wide cap on simultaneous open positions across all bots.
	// Zero = no limit enforced here (trust each bot's own PositionConfig.MaxPositions).
	MaxPositions int `json:"max_positions,omitempty"`

	// MaxPositionPct is the maximum fraction of total equity any single open position may occupy.
	// Applied by the risk manager before each order (e.g. 0.10 = 10%).
	MaxPositionPct float64 `json:"max_position_pct,omitempty"`

	// ReserveRatio is the fraction of total capital kept as uninvested cash reserve.
	// Hands cannot deploy capital beyond (1 - ReserveRatio) × TotalCapital.
	// E.g. 0.10 = always keep 10% in cash.
	ReserveRatio float64 `json:"reserve_ratio,omitempty"`
}

// Defaults fills zero-value PortfolioConfig fields with sensible values.
func (p *PortfolioConfig) Defaults() {
	if p.MaxPositions == 0 {
		p.MaxPositions = 5
	}
	if p.MaxPositionPct == 0 {
		p.MaxPositionPct = 0.10
	}
	// ReserveRatio defaults to 0 (no forced cash reserve).
}

// RiskConfig holds account-level circuit-breakers — pure risk guards, no sizing.
// Sizing lives in PortfolioConfig; per-bot exit rules live in bot.BotRiskConfig.
type RiskConfig struct {
	// DailyLossLimitPct halts trading for the rest of the day when daily PnL loss
	// exceeds this fraction of equity (e.g. 0.02 = 2%).
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct,omitempty"`

	// MaxDrawdownPct permanently halts all trading when the portfolio drawdown from
	// peak equity exceeds this fraction (e.g. 0.10 = 10%).
	MaxDrawdownPct float64 `json:"max_drawdown_pct,omitempty"`
}

// Defaults fills zero-value RiskConfig fields with sensible values.
func (r *RiskConfig) Defaults() {
	if r.DailyLossLimitPct == 0 {
		r.DailyLossLimitPct = 0.02
	}
	if r.MaxDrawdownPct == 0 {
		r.MaxDrawdownPct = 0.10
	}
}
