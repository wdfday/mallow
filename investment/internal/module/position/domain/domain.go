package domain

import (
	"time"

	"github.com/google/uuid"
)

// PortfolioPosition is the read model for current spot holdings.
type PortfolioPosition struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;index;column:account_id" json:"account_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	// Asset identification
	Symbol     string `gorm:"type:varchar(20);not null;column:symbol" json:"symbol"`
	Name       string `gorm:"type:varchar(255);column:name" json:"name"`
	AssetType  string `gorm:"type:varchar(30);column:asset_type" json:"asset_type"`
	AssetClass string `gorm:"type:varchar(50);column:asset_class" json:"asset_class,omitempty"`
	Exchange   string `gorm:"type:varchar(50);column:exchange" json:"exchange,omitempty"`
	Currency   string `gorm:"type:varchar(3);not null;default:'USD';column:currency" json:"currency"`

	// Holding
	Quantity  float64 `gorm:"type:decimal(20,8);not null;default:0;column:quantity" json:"quantity"`
	AvgCost   float64 `gorm:"type:decimal(15,2);not null;default:0;column:avg_cost" json:"avg_cost"`
	TotalCost float64 `gorm:"type:decimal(15,2);not null;default:0;column:total_cost" json:"total_cost"`

	// Market
	CurrentPrice  float64 `gorm:"type:decimal(15,2);not null;default:0;column:current_price" json:"current_price"`
	CurrentValue  float64 `gorm:"type:decimal(15,2);not null;default:0;column:current_value" json:"current_value"`
	UnrealizedPnL float64 `gorm:"type:decimal(15,2);not null;default:0;column:unrealized_pnl" json:"unrealized_pnl"`
	UnrealizedPct float64 `gorm:"type:decimal(10,4);not null;default:0;column:unrealized_pct" json:"unrealized_pct"`

	// Realized
	RealizedPnL     float64 `gorm:"type:decimal(15,2);not null;default:0;column:realized_pnl" json:"realized_pnl"`
	TotalDividends  float64 `gorm:"type:decimal(15,2);not null;default:0;column:total_dividends" json:"total_dividends"`
	PortfolioWeight float64 `gorm:"type:decimal(10,4);not null;default:0;column:portfolio_weight" json:"portfolio_weight"`

	// Status
	Status  string `gorm:"type:varchar(20);not null;default:'active';column:status" json:"status"`
	LastSeq int64  `gorm:"not null;default:0;column:last_seq" json:"last_seq"` // idempotency guard

	OpenedAt  time.Time  `gorm:"column:opened_at" json:"opened_at"`
	ClosedAt  *time.Time `gorm:"column:closed_at" json:"closed_at,omitempty"`
	UpdatedAt time.Time  `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
}

func (PortfolioPosition) TableName() string {
	return "portfolio_positions"
}

// CalculateMetrics recomputes derived fields.
func (p *PortfolioPosition) CalculateMetrics() {
	p.CurrentValue = p.Quantity * p.CurrentPrice
	p.UnrealizedPnL = p.CurrentValue - p.TotalCost
	if p.TotalCost > 0 {
		p.UnrealizedPct = (p.UnrealizedPnL / p.TotalCost) * 100
	}
}

// AddQuantity updates avg_cost and total_cost on a buy.
func (p *PortfolioPosition) AddQuantity(qty, pricePerUnit float64) {
	newCost := p.TotalCost + qty*pricePerUnit
	newQty := p.Quantity + qty
	if newQty > 0 {
		p.AvgCost = newCost / newQty
	}
	p.Quantity = newQty
	p.TotalCost = newCost
	p.CalculateMetrics()
}

// RemoveQuantity updates fields on a sell and returns realized PnL.
func (p *PortfolioPosition) RemoveQuantity(qty, pricePerUnit float64) float64 {
	if qty > p.Quantity {
		qty = p.Quantity
	}
	costBasis := qty * p.AvgCost
	proceeds := qty * pricePerUnit
	realized := proceeds - costBasis

	p.Quantity -= qty
	p.TotalCost -= costBasis
	p.RealizedPnL += realized
	p.CalculateMetrics()

	if p.Quantity == 0 {
		p.Status = "closed"
		now := time.Now().UTC()
		p.ClosedAt = &now
	}
	return realized
}
