package domain

import (
	"time"

	"github.com/google/uuid"
)

// HelmStatus identifies the current operational state of a Helm orchestrator.
type HelmStatus string

const (
	// HelmStatusActive is the normal running state — hands accept signals and place orders.
	HelmStatusActive HelmStatus = "active"
	// HelmStatusPaused is set by the user (or broker deactivation). All hands are
	// cascade-stopped in-memory; no new orders. Resume restores the previous hand set.
	HelmStatusPaused HelmStatus = "paused"
	// HelmStatusHalted is set by the risk circuit-breaker (daily loss / drawdown) or by
	// /kill. All positions are flattened. Requires /halt/reset to recover (manual intent).
	HelmStatusHalted HelmStatus = "halted"
	// HelmStatusDisabled is set by the user via /disable. All positions are flattened and
	// the runtime is torn down. Requires /enable to re-spawn (destructive — not reversible
	// without re-spawning). Does NOT require admin access.
	HelmStatusDisabled HelmStatus = "disabled"
	// HelmStatusError is set when the exchange rejects the stored credentials mid-run
	// (ErrClassAuth after 3 consecutive failures). The helm is paused in-memory and the
	// linked broker connection is marked error. Recover by rotating the API key
	// (POST /broker-connections/:id/rotate-key) which auto-resumes the helm.
	HelmStatusError HelmStatus = "error"
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
	Status       HelmStatus `json:"status"`         // active | paused | halted | disabled | error
	LastSyncedAt *time.Time `json:"last_synced_at"` // persisted after each successful REST sync
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}
