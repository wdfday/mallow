package domain

import (
	"time"

	"github.com/google/uuid"
)

// PortfolioCashFlow is the read model for dividends, deposits, withdrawals, and fees.
type PortfolioCashFlow struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;index;column:account_id" json:"account_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	FlowType string  `gorm:"type:varchar(20);not null;index;column:flow_type" json:"flow_type"` // dividend|deposit|withdrawal|fee
	Amount   float64 `gorm:"type:decimal(15,2);not null;column:amount" json:"amount"`
	Currency string  `gorm:"type:varchar(3);not null;default:'USD';column:currency" json:"currency"`

	Symbol      string `gorm:"type:varchar(20);column:symbol" json:"symbol,omitempty"` // nullable — dividends have symbol, deposits don't
	Description string `gorm:"type:text;column:description" json:"description,omitempty"`

	SourceEventID uuid.UUID `gorm:"type:uuid;column:source_event_id" json:"source_event_id"`

	OccurredAt time.Time `gorm:"not null;index;column:occurred_at" json:"occurred_at"`
	CreatedAt  time.Time `gorm:"autoCreateTime;column:created_at" json:"created_at"`
}

func (PortfolioCashFlow) TableName() string {
	return "portfolio_cash_flows"
}
