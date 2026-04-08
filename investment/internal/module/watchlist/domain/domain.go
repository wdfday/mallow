package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// WatchlistItem is a simple CRUD entry (not event-sourced).
type WatchlistItem struct {
	ID     uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	Symbol      string   `gorm:"type:varchar(20);not null;column:symbol" json:"symbol"`
	Name        string   `gorm:"type:varchar(255);column:name" json:"name,omitempty"`
	AssetType   string   `gorm:"type:varchar(30);column:asset_type" json:"asset_type,omitempty"`
	TargetPrice *decimal.Decimal `gorm:"type:decimal(15,2);column:target_price" json:"target_price,omitempty"`
	Notes       string   `gorm:"type:text;column:notes" json:"notes,omitempty"`

	AddedAt   time.Time `gorm:"autoCreateTime;column:added_at" json:"added_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
}

func (WatchlistItem) TableName() string {
	return "investment_watchlist"
}
