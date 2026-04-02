package domain

import (
	"time"

	"github.com/google/uuid"
)

// OrchestratorConfig is the persisted configuration of one orchestrator instance.
// 1:1 with an investment account (via AccountID).
// Auto-created on account.linked event; never manually created/deleted via API.
// Runtime state (portfolio, orderbook, running bots) lives in runtime.OrchestratorRuntime.
type OrchestratorConfig struct {
	ID           uuid.UUID      `json:"id"`
	UserID       uuid.UUID      `json:"user_id"`    // owner — from identity.users.id
	AccountID    uuid.UUID      `json:"account_id"` // → investment.accounts.id
	Name         string         `json:"name"`
	Capital      float64        `json:"capital"`
	Exchange     ExchangeConfig `json:"exchange"`
	Risk         RiskConfig     `json:"risk"`
	Enabled      bool           `json:"enabled"`        // user toggle — gates bot create/delete
	Status       string         `json:"status"`         // active | paused | halted (runtime state)
	LastSyncedAt *time.Time     `json:"last_synced_at"` // persisted after each successful REST sync
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
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

// RiskConfig is the portfolio-level risk configuration for one orchestrator.
type RiskConfig struct {
	MaxPositions      int     `json:"max_positions"`
	MaxPositionPct    float64 `json:"max_position_pct"`
	DailyLossLimitPct float64 `json:"daily_loss_limit_pct"`
	MaxDrawdownPct    float64 `json:"max_drawdown_pct"`
}

// Defaults fills zero-value RiskConfig fields with sensible values.
func (r *RiskConfig) Defaults() {
	if r.MaxPositions == 0 {
		r.MaxPositions = 5
	}
	if r.MaxPositionPct == 0 {
		r.MaxPositionPct = 0.10
	}
	if r.DailyLossLimitPct == 0 {
		r.DailyLossLimitPct = 0.02
	}
	if r.MaxDrawdownPct == 0 {
		r.MaxDrawdownPct = 0.10
	}
}

// OrchestratorRepo is the port for persisting and retrieving orchestrator configs.
type OrchestratorRepo interface {
	Save(o *OrchestratorConfig) error
	Get(id uuid.UUID) (*OrchestratorConfig, error)
	GetByAccountID(accountID uuid.UUID) (*OrchestratorConfig, error)
	All() ([]*OrchestratorConfig, error)
	AllByUser(userID uuid.UUID) ([]*OrchestratorConfig, error)
	Update(id uuid.UUID, fn func(*OrchestratorConfig) error) error
	UpdateLastSyncedAt(id uuid.UUID, t time.Time) error
	Delete(id uuid.UUID) error
}
