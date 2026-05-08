package domain

import (
	"time"

	"github.com/google/uuid"
)

// Hand is the persisted + runtime shape of a hand.
// It is the single source of truth — used directly as a GORM model (TableName = "hands")
// and passed through the service and runtime layers without an extra Config wrapper.
type Hand struct {
	// Identity
	ID     uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	HelmID uuid.UUID `gorm:"column:helm_id;type:uuid;not null;index:idx_hands_helm_id"`

	// Scalar columns
	Name   string     `gorm:"column:name;not null"`
	Type   HandType   `gorm:"column:type;not null;default:signal_follower"`
	Market MarketType `gorm:"column:market;not null;default:spot"`
	Status string     `gorm:"column:status;not null;default:stopped"`

	// JSONB columns — each type implements driver.Valuer + sql.Scanner
	Symbols  StringSlice    `gorm:"column:symbols;type:jsonb;not null;default:'[]'"`
	Strategy StrategySpec   `gorm:"column:strategy;type:jsonb;not null;default:'{}'"`
	Position PositionConfig `gorm:"column:position;type:jsonb;not null;default:'{}'"`
	Risk     HandRiskConfig `gorm:"column:risk;type:jsonb;not null;default:'{}'"`
	Futures  *FuturesConfig `gorm:"column:futures;type:jsonb"`

	// Timestamps
	CreatedAt time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (Hand) TableName() string { return "hands" }

// ApplyConfig copies all fields from a HandConfig onto the hand.
// Call after setting ID, HelmID, Status, CreatedAt.
func (b *Hand) ApplyConfig(cfg HandConfig) {
	b.HelmID = cfg.HelmID
	b.Name = cfg.Name
	b.Type = cfg.Type
	b.Market = cfg.Market
	b.Symbols = StringSlice(cfg.Symbols)
	b.Strategy = cfg.Strategy
	b.Position = cfg.Position
	b.Risk = cfg.Risk
	b.Futures = cfg.Futures
}
