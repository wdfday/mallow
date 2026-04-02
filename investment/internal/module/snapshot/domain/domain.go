package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// PortfolioSnapshot is the read model for time-series portfolio performance.
type PortfolioSnapshot struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;index;column:account_id" json:"account_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	SnapshotDate time.Time `gorm:"not null;index;column:snapshot_date" json:"snapshot_date"`
	SnapshotType string    `gorm:"type:varchar(20);not null;index;column:snapshot_type" json:"snapshot_type"` // daily|weekly|monthly|manual

	TotalValue      float64 `gorm:"type:decimal(15,2);not null;column:total_value" json:"total_value"`
	CashBalance     float64 `gorm:"type:decimal(15,2);default:0;column:cash_balance" json:"cash_balance"`
	SpotValue       float64 `gorm:"type:decimal(15,2);default:0;column:spot_value" json:"spot_value"`
	DerivativeValue float64 `gorm:"type:decimal(15,2);default:0;column:derivative_value" json:"derivative_value"`
	TotalCost       float64 `gorm:"type:decimal(15,2);not null;column:total_cost" json:"total_cost"`
	UnrealizedPnL   float64 `gorm:"type:decimal(15,2);not null;column:unrealized_pnl" json:"unrealized_pnl"`
	RealizedPnL     float64 `gorm:"type:decimal(15,2);not null;column:realized_pnl" json:"realized_pnl"`
	TotalDividends  float64 `gorm:"type:decimal(15,2);default:0;column:total_dividends" json:"total_dividends"`
	TotalFees       float64 `gorm:"type:decimal(15,2);default:0;column:total_fees" json:"total_fees"`

	TotalReturn    float64 `gorm:"type:decimal(15,2);not null;column:total_return" json:"total_return"`
	TotalReturnPct float64 `gorm:"type:decimal(10,4);not null;column:total_return_pct" json:"total_return_pct"`
	DayChange      float64 `gorm:"type:decimal(15,2);default:0;column:day_change" json:"day_change"`
	DayChangePct   float64 `gorm:"type:decimal(10,4);default:0;column:day_change_pct" json:"day_change_pct"`

	CashInflow  float64 `gorm:"type:decimal(15,2);default:0;column:cash_inflow" json:"cash_inflow"`
	CashOutflow float64 `gorm:"type:decimal(15,2);default:0;column:cash_outflow" json:"cash_outflow"`

	SpotAllocation   json.RawMessage `gorm:"type:jsonb;column:spot_allocation" json:"spot_allocation,omitempty"`
	DerivativeAlloc  json.RawMessage `gorm:"type:jsonb;column:derivative_allocation" json:"derivative_allocation,omitempty"`
	SectorAllocation json.RawMessage `gorm:"type:jsonb;column:sector_allocation" json:"sector_allocation,omitempty"`
	Metrics          json.RawMessage `gorm:"type:jsonb;column:metrics" json:"metrics,omitempty"` // {sharpe,sortino,beta,max_drawdown,volatility}

	SourceEventID uuid.UUID `gorm:"type:uuid;column:source_event_id" json:"source_event_id"`

	CreatedAt time.Time `gorm:"autoCreateTime;column:created_at" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
}

func (PortfolioSnapshot) TableName() string {
	return "portfolio_snapshots"
}
