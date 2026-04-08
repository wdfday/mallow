package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BotSummary is the JSON-safe public view of a bot instance.
type BotSummary struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Type           BotType        `json:"type"`
	OrchestratorID uuid.UUID      `json:"orchestrator_id"`
	Strategy       Strategy       `json:"tactic"`
	Risk           BotRiskConfig  `json:"risk"`
	Symbols        []string       `json:"symbols"`
	Status         string         `json:"status"`
	Running        bool           `json:"running"`
	OrderCount     int            `json:"order_count"`
	Health         BotHealthView  `json:"health"`
	Metrics        BotMetricsView `json:"metrics"`
	CreatedAt      time.Time      `json:"created_at"`
}

// BotHealthView is the JSON-safe health snapshot.
type BotHealthView struct {
	Status       string `json:"status"`
	StartedAt    string `json:"started_at,omitempty"`
	LastSignalAt string `json:"last_signal_at,omitempty"`
	LastOrderAt  string `json:"last_order_at,omitempty"`
	LastErrorAt  string `json:"last_error_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	Uptime       string `json:"uptime,omitempty"`
}

// BotMetricsView is the JSON-safe metrics snapshot.
type BotMetricsView struct {
	SignalsReceived int64   `json:"signals_received"`
	SignalsFiltered int64   `json:"signals_filtered"`
	TradesApproved  int64   `json:"trades_approved"`
	OrdersPlaced    int64   `json:"orders_placed"`
	OrdersFilled    int64   `json:"orders_filled"`
	OrdersFailed    int64   `json:"orders_failed"`
	TotalPnL        decimal.Decimal `json:"total_pnl"`
	WinCount        int64   `json:"win_count"`
	LossCount       int64   `json:"loss_count"`
}
