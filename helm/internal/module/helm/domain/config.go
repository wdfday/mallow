package domain

import (
	"time"

	"github.com/google/uuid"
)

// HelmConfig Orchestrator is the persisted configuration of one orchestrator instance.
// 1:1 with an investment account (via AccountID).
// Auto-created on account.linked event; never manually created/deleted via API.
// Runtime state (portfolio, orderbook, running bots) lives in runtime.OrchestratorRuntime.
type HelmConfig struct {
	ID           uuid.UUID       `json:"id"`
	UserID       uuid.UUID       `json:"user_id"`    // owner — from identity.users.id
	AccountID    uuid.UUID       `json:"account_id"` // → investment.accounts.id
	Name         string          `json:"name"`
	Capital      float64         `json:"capital"`
	Exchange     ExchangeConfig  `json:"exchange"`
	Portfolio    PortfolioConfig `json:"portfolio"`      // capital allocation at account level
	Risk         RiskConfig      `json:"risk"`           // circuit-breakers / drawdown guards
	Enabled      bool            `json:"enabled"`        // user toggle — gates hand create/delete
	Status       string          `json:"status"`         // active | paused | halted (runtime state)
	LastSyncedAt *time.Time      `json:"last_synced_at"` // persisted after each successful REST sync
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// ExchangeConfig holds broker connection credentials for this orchestrator.
// Sourced from investment.broker_connections; copied here so the orchestrator is self-contained.
type ExchangeConfig struct {
	BrokerType string `json:"broker_type"` // alpaca | binance | okx | bybit | ibkr | oanda
	APIKey     string `json:"api_key,omitempty"`
	APISecret  string `json:"api_secret,omitempty"`
	Passphrase string `json:"passphrase,omitempty"` // OKX
	AccountID  string `json:"account_id,omitempty"` // IBKR / Oanda
	BaseURL    string `json:"base_url,omitempty"`
	StreamURL  string `json:"stream_url,omitempty"` // Oanda
	Demo       bool   `json:"demo,omitempty"`
	Testnet    bool   `json:"testnet,omitempty"`
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

// HelmRepo is the port for persisting and retrieving orchestrator configs.
type HelmRepo interface {
	Save(o *HelmConfig) error
	Get(id uuid.UUID) (*HelmConfig, error)
	GetByAccountID(accountID uuid.UUID) (*HelmConfig, error)
	All() ([]*HelmConfig, error)
	AllByUser(userID uuid.UUID) ([]*HelmConfig, error)
	Update(id uuid.UUID, fn func(*HelmConfig) error) error
	UpdateLastSyncedAt(id uuid.UUID, t time.Time) error
	Delete(id uuid.UUID) error
}
