package event

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// PortfolioSnapshotTaken is emitted by the daily cron or a manual trigger.
type PortfolioSnapshotTaken struct {
	UserID           uuid.UUID       `json:"user_id"`
	SnapshotDate     time.Time       `json:"snapshot_date"`
	SnapshotType     SnapshotType    `json:"snapshot_type"`
	TotalValue       float64         `json:"total_value"`
	CashBalance      float64         `json:"cash_balance"`
	SpotValue        float64         `json:"spot_value"`
	DerivativeValue  float64         `json:"derivative_value"`
	TotalCost        float64         `json:"total_cost"`
	UnrealizedPnL    float64         `json:"unrealized_pnl"`
	RealizedPnL      float64         `json:"realized_pnl"`
	TotalDividends   float64         `json:"total_dividends"`
	TotalFees        float64         `json:"total_fees"`
	TotalReturn      float64         `json:"total_return"`
	TotalReturnPct   float64         `json:"total_return_pct"`
	DayChange        float64         `json:"day_change"`
	DayChangePct     float64         `json:"day_change_pct"`
	CashInflow       float64         `json:"cash_inflow"`
	CashOutflow      float64         `json:"cash_outflow"`
	SpotAllocation   json.RawMessage `json:"spot_allocation,omitempty"`
	DerivativeAlloc  json.RawMessage `json:"derivative_allocation,omitempty"`
	SectorAllocation json.RawMessage `json:"sector_allocation,omitempty"`
	Metrics          json.RawMessage `json:"metrics,omitempty"` // sharpe, sortino, beta, max_drawdown, volatility
}
