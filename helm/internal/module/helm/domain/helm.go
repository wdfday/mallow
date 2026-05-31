package domain

import (
	"time"

	"github.com/google/uuid"
)

// HelmStatus identifies the current operational state of a Helm orchestrator.
type HelmStatus string

const (
	HelmStatusActive   HelmStatus = "active"
	HelmStatusPaused   HelmStatus = "paused"
	HelmStatusHalted   HelmStatus = "halted"
	HelmStatusDisabled HelmStatus = "disabled"
)

// Helm is the persisted configuration of one orchestrator instance.
// 1:1 with an investment account (via AccountID).
// Auto-created on account.linked event; never manually created/deleted via API.
// Runtime state (portfolio, orderbook, running bots) lives in runtime.OrchestratorRuntime.
// Capital and exchange credentials are NOT persisted — fetched transiently from investment service.
type Helm struct {
	ID           uuid.UUID  `json:"id"`
	UserID       uuid.UUID  `json:"user_id"`    // owner — from identity.users.id
	AccountID    uuid.UUID  `json:"account_id"` // → investment.accounts.id
	Name         string     `json:"name"`
	BrokerType   string     `json:"broker_type"`    // alpaca | binance | okx | bybit — persisted for routing
	AccountType  string     `json:"account_type"`   // spot | futures_usdm | futures_coinm | unified
	Risk         RiskConfig `json:"risk"`           // account-level guards: max positions, daily loss, drawdown
	Status       HelmStatus `json:"status"`         // active | paused | halted | disabled
	LastSyncedAt *time.Time `json:"last_synced_at"` // persisted after each successful REST sync
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}
