package domain

import (
	"time"

	"github.com/google/uuid"
)

// Helm is the persisted configuration of one orchestrator instance.
// 1:1 with an investment account (via AccountID).
// Auto-created on account.linked event; never manually created/deleted via API.
// Runtime state (portfolio, orderbook, running bots) lives in runtime.OrchestratorRuntime.
// Capital and exchange credentials are NOT persisted — fetched transiently from investment service.
type Helm struct {
	ID           uuid.UUID       `json:"id"`
	UserID       uuid.UUID       `json:"user_id"`    // owner — from identity.users.id
	AccountID    uuid.UUID       `json:"account_id"` // → investment.accounts.id
	Name         string          `json:"name"`
	BrokerType   string          `json:"broker_type"`    // alpaca | binance | okx | bybit — persisted for routing
	AccountType  string          `json:"account_type"`   // spot | futures_usdm | futures_coinm | unified
	Portfolio    PortfolioConfig `json:"portfolio"`      // capital allocation at account level
	Risk         RiskConfig      `json:"risk"`           // circuit-breakers / drawdown guards
	Enabled      bool            `json:"enabled"`        // user toggle — gates hand create/delete
	Status       string          `json:"status"`         // active | paused | halted (runtime state)
	LastSyncedAt *time.Time      `json:"last_synced_at"` // persisted after each successful REST sync
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}
