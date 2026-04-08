package event

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PortfolioSnapshotTaken is emitted by the daily cron or a manual trigger.
type PortfolioSnapshotTaken struct {
	UserID           uuid.UUID       `json:"user_id"`
	SnapshotDate     time.Time       `json:"snapshot_date"`
	SnapshotType     SnapshotType    `json:"snapshot_type"`
	TotalValue       decimal.Decimal `json:"total_value"`
	CashBalance      decimal.Decimal `json:"cash_balance"`
	SpotValue        decimal.Decimal `json:"spot_value"`
	DerivativeValue  decimal.Decimal `json:"derivative_value"`
	TotalCost        decimal.Decimal `json:"total_cost"`
	UnrealizedPnL    decimal.Decimal `json:"unrealized_pnl"`
	RealizedPnL      decimal.Decimal `json:"realized_pnl"`
	TotalDividends   decimal.Decimal `json:"total_dividends"`
	TotalFees        decimal.Decimal `json:"total_fees"`
	TotalReturn      decimal.Decimal `json:"total_return"`
	TotalReturnPct   float64         `json:"total_return_pct"`
	DayChange        decimal.Decimal `json:"day_change"`
	DayChangePct     float64         `json:"day_change_pct"`
	CashInflow       decimal.Decimal `json:"cash_inflow"`
	CashOutflow      decimal.Decimal `json:"cash_outflow"`
	SpotAllocation   json.RawMessage `json:"spot_allocation,omitempty"`
	DerivativeAlloc  json.RawMessage `json:"derivative_allocation,omitempty"`
	SectorAllocation json.RawMessage `json:"sector_allocation,omitempty"`
	Metrics          json.RawMessage `json:"metrics,omitempty"` // sharpe, sortino, beta, max_drawdown, volatility
}
