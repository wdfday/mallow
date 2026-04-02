package domain

import (
	"time"

	"github.com/google/uuid"
)

// DerivativePosition is the read model for futures/options/perps/swaps.
type DerivativePosition struct {
	ID        uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	AccountID uuid.UUID `gorm:"type:uuid;not null;index;column:account_id" json:"account_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	Symbol         string `gorm:"type:varchar(20);not null;column:symbol" json:"symbol"`
	Underlying     string `gorm:"type:varchar(20);column:underlying" json:"underlying,omitempty"`
	InstrumentType string `gorm:"type:varchar(20);not null;column:instrument_type" json:"instrument_type"` // futures|perp|options|swap
	Side           string `gorm:"type:varchar(10);not null;column:side" json:"side"`                       // long|short
	Currency       string `gorm:"type:varchar(3);not null;default:'USD';column:currency" json:"currency"`

	Quantity         float64  `gorm:"type:decimal(20,8);not null;column:quantity" json:"quantity"`
	EntryPrice       float64  `gorm:"type:decimal(15,2);not null;column:entry_price" json:"entry_price"`
	CurrentPrice     float64  `gorm:"type:decimal(15,2);default:0;column:current_price" json:"current_price"`
	Leverage         float64  `gorm:"type:decimal(10,2);default:1;column:leverage" json:"leverage"`
	MarginUsed       float64  `gorm:"type:decimal(15,2);default:0;column:margin_used" json:"margin_used"`
	LiquidationPrice *float64 `gorm:"type:decimal(15,2);column:liquidation_price" json:"liquidation_price,omitempty"`
	ContractSize     float64  `gorm:"type:decimal(15,4);default:1;column:contract_size" json:"contract_size"`

	// Options-specific
	StrikePrice *float64 `gorm:"type:decimal(15,2);column:strike_price" json:"strike_price,omitempty"`
	OptionType  string   `gorm:"type:varchar(10);column:option_type" json:"option_type,omitempty"` // call|put
	ExpiryDate  *string  `gorm:"type:date;column:expiry_date" json:"expiry_date,omitempty"`

	UnrealizedPnL float64 `gorm:"type:decimal(15,2);default:0;column:unrealized_pnl" json:"unrealized_pnl"`
	RealizedPnL   float64 `gorm:"type:decimal(15,2);default:0;column:realized_pnl" json:"realized_pnl"`

	Status   string     `gorm:"type:varchar(20);not null;default:'open';column:status" json:"status"` // open|closed
	OpenedAt time.Time  `gorm:"not null;column:opened_at" json:"opened_at"`
	ClosedAt *time.Time `gorm:"column:closed_at" json:"closed_at,omitempty"`

	// Source event
	OpenEventID  uuid.UUID  `gorm:"type:uuid;column:open_event_id" json:"open_event_id"`
	CloseEventID *uuid.UUID `gorm:"type:uuid;column:close_event_id" json:"close_event_id,omitempty"`

	UpdatedAt time.Time `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
}

func (DerivativePosition) TableName() string {
	return "derivative_positions"
}
