package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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
	Quantity  decimal.Decimal `gorm:"type:decimal(20,8);not null;default:0;column:quantity" json:"quantity"`
	AvgCost   decimal.Decimal `gorm:"type:decimal(15,2);not null;default:0;column:avg_cost" json:"avg_cost"`
	TotalCost decimal.Decimal `gorm:"type:decimal(15,2);not null;default:0;column:total_cost" json:"total_cost"`

	// Market
	CurrentPrice  decimal.Decimal `gorm:"type:decimal(15,2);not null;default:0;column:current_price" json:"current_price"`
	CurrentValue  decimal.Decimal `gorm:"type:decimal(15,2);not null;default:0;column:current_value" json:"current_value"`
	UnrealizedPnL decimal.Decimal `gorm:"type:decimal(15,2);not null;default:0;column:unrealized_pnl" json:"unrealized_pnl"`
	UnrealizedPct float64         `gorm:"type:decimal(10,4);not null;default:0;column:unrealized_pct" json:"unrealized_pct"`

	// Realized
	RealizedPnL     decimal.Decimal `gorm:"type:decimal(15,2);not null;default:0;column:realized_pnl" json:"realized_pnl"`
	TotalDividends  decimal.Decimal `gorm:"type:decimal(15,2);not null;default:0;column:total_dividends" json:"total_dividends"`
	PortfolioWeight float64         `gorm:"type:decimal(10,4);not null;default:0;column:portfolio_weight" json:"portfolio_weight"`

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
	p.CurrentValue = p.Quantity.Mul(p.CurrentPrice)
	p.UnrealizedPnL = p.CurrentValue.Sub(p.TotalCost)
	if p.TotalCost.IsPositive() {
		p.UnrealizedPct = p.UnrealizedPnL.Div(p.TotalCost).Mul(decimal.NewFromInt(100)).InexactFloat64()
	}
}

// AddQuantity updates avg_cost and total_cost on a buy.
func (p *PortfolioPosition) AddQuantity(qty, pricePerUnit decimal.Decimal) {
	newCost := p.TotalCost.Add(qty.Mul(pricePerUnit))
	newQty := p.Quantity.Add(qty)
	if newQty.IsPositive() {
		p.AvgCost = newCost.Div(newQty)
	}
	p.Quantity = newQty
	p.TotalCost = newCost
	p.CalculateMetrics()
}

// RemoveQuantity updates fields on a sell and returns realized PnL.
func (p *PortfolioPosition) RemoveQuantity(qty, pricePerUnit decimal.Decimal) decimal.Decimal {
	if qty.GreaterThan(p.Quantity) {
		qty = p.Quantity
	}
	costBasis := qty.Mul(p.AvgCost)
	proceeds := qty.Mul(pricePerUnit)
	realized := proceeds.Sub(costBasis)

	p.Quantity = p.Quantity.Sub(qty)
	p.TotalCost = p.TotalCost.Sub(costBasis)
	p.RealizedPnL = p.RealizedPnL.Add(realized)
	p.CalculateMetrics()

	if p.Quantity.IsZero() {
		p.Status = "closed"
		now := time.Now().UTC()
		p.ClosedAt = &now
	}
	return realized
}
