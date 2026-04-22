package domain

import (
	"time"

	"github.com/google/uuid"
)

// BotInstance is the persisted + runtime shape of a bot.
// It is the single source of truth — used directly as a GORM model (TableName = "bots")
// and passed through the service and runtime layers without an extra Config wrapper.
type BotInstance struct {
	// Identity
	ID             string    `gorm:"column:id;primaryKey"`
	OrchestratorID uuid.UUID `gorm:"column:orchestrator_id;not null;index:idx_bots_orchestrator_id"`

	// Scalar columns
	Name   string     `gorm:"column:name;not null"`
	Type   BotType    `gorm:"column:type;not null;default:signal_follower"`
	Market MarketType `gorm:"column:market;not null;default:spot"`
	Status string     `gorm:"column:status;not null;default:stopped"`

	// JSONB columns — each type implements driver.Valuer + sql.Scanner
	Symbols  StringSlice    `gorm:"column:symbols;type:jsonb;not null;default:'[]'"`
	Strategy StrategyConfig `gorm:"column:strategy;type:jsonb;not null;default:'{}'"`
	Position PositionConfig `gorm:"column:position;type:jsonb;not null;default:'{}'"`
	Risk     BotRiskConfig  `gorm:"column:risk;type:jsonb;not null;default:'{}'"`
	Futures  *FuturesConfig `gorm:"column:futures;type:jsonb"`

	// Timestamps
	CreatedAt time.Time `gorm:"column:created_at;not null;autoCreateTime"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;autoUpdateTime"`
}

func (BotInstance) TableName() string { return "bots" }

// ApplyConfig copies all fields from a BotConfig onto this instance.
// Call after setting ID, OrchestratorID, Status, CreatedAt.
func (b *BotInstance) ApplyConfig(cfg BotConfig) {
	b.OrchestratorID = cfg.OrchestratorID
	b.Name = cfg.Name
	b.Type = cfg.Type
	b.Market = cfg.Market
	b.Symbols = StringSlice(cfg.Symbols)
	b.Strategy = cfg.Strategy
	b.Position = cfg.Position
	b.Risk = cfg.Risk
	b.Futures = cfg.Futures
}
