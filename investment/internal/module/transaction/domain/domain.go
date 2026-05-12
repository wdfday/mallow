package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// PortfolioTransaction is the read model for transaction history.
type PortfolioTransaction struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;index;column:account_id" json:"account_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	Symbol   string `gorm:"type:varchar(20);column:symbol" json:"symbol,omitempty"`
	TxType   string `gorm:"type:varchar(30);not null;index;column:tx_type" json:"tx_type"` // buy|sell|dividend|...
	Currency string `gorm:"type:varchar(3);not null;default:'USD';column:currency" json:"currency"`

	Quantity    decimal.Decimal  `gorm:"type:decimal(20,8);column:quantity" json:"quantity,omitempty"`
	Price       decimal.Decimal  `gorm:"type:decimal(15,2);column:price" json:"price,omitempty"`
	Amount      decimal.Decimal  `gorm:"type:decimal(15,2);not null;column:amount" json:"amount"`
	Fees        decimal.Decimal  `gorm:"type:decimal(15,2);default:0;column:fees" json:"fees"`
	Commission  decimal.Decimal  `gorm:"type:decimal(15,2);default:0;column:commission" json:"commission"`
	Tax         decimal.Decimal  `gorm:"type:decimal(15,2);default:0;column:tax" json:"tax"`
	RealizedPnL *decimal.Decimal `gorm:"type:decimal(15,2);column:realized_pnl" json:"realized_pnl,omitempty"`

	ExternalID string `gorm:"type:varchar(255);index;column:external_id" json:"external_id,omitempty"`
	Source     string `gorm:"type:varchar(20);column:source" json:"source,omitempty"` // manual|sync
	BotID      string `gorm:"type:varchar(100);index;column:bot_id" json:"bot_id,omitempty"`
	Notes      string `gorm:"type:text;column:notes" json:"notes,omitempty"`

	// Source event
	SourceEventID uuid.UUID `gorm:"type:uuid;column:source_event_id" json:"source_event_id"`

	TxDate    time.Time `gorm:"not null;index;column:tx_date" json:"tx_date"`
	CreatedAt time.Time `gorm:"autoCreateTime;column:created_at" json:"created_at"`
}

func (PortfolioTransaction) TableName() string {
	return "trades"
}
