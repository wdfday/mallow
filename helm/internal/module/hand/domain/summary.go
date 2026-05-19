package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// HandSummary is the JSON-safe public view of a hand instance.
type HandSummary struct {
	ID               uuid.UUID       `json:"id"`
	Name             string          `json:"name"`
	Type             HandType        `json:"type"`
	Market           MarketType      `json:"market"`
	HelmID           uuid.UUID       `json:"helm_id"`
	Strategy         StrategySpec    `json:"strategy"`
	Position         PositionConfig  `json:"position"`
	Risk             HandRiskConfig  `json:"risk"`
	Symbols          []string        `json:"symbols"`
	Status           HandStatus      `json:"status"`
	Running          bool            `json:"running"`
	OrderCount       int             `json:"order_count"`
	Health           HandHealthView  `json:"health"`
	Metrics          HandMetricsView `json:"metrics"`
	Futures          *FuturesConfig  `json:"futures,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	DeployedCapital  decimal.Decimal `json:"deployed_capital"`
	AllocatedCapital decimal.Decimal `json:"allocated_capital"`
	SignalTTLSec     int             `json:"signal_ttl_sec,omitempty"`
}

// HandHealthView is the JSON-safe health snapshot.
type HandHealthView struct {
	Status       string `json:"status"`
	StartedAt    string `json:"started_at,omitempty"`
	LastSignalAt string `json:"last_signal_at,omitempty"`
	LastOrderAt  string `json:"last_order_at,omitempty"`
	LastErrorAt  string `json:"last_error_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	Uptime       string `json:"uptime,omitempty"`
}

// HandMetricsView is the JSON-safe metrics snapshot.
type HandMetricsView struct {
	SignalsReceived int64           `json:"signals_received"`
	SignalsFiltered int64           `json:"signals_filtered"`
	TradesApproved  int64           `json:"trades_approved"`
	OrdersPlaced    int64           `json:"orders_placed"`
	OrdersFilled    int64           `json:"orders_filled"`
	OrdersFailed    int64           `json:"orders_failed"`
	TotalPnL        decimal.Decimal `json:"total_pnl"`
	WinCount        int64           `json:"win_count"`
	LossCount       int64           `json:"loss_count"`
}
