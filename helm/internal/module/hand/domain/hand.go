package domain

import (
	"time"

	"github.com/google/uuid"
)

// Hand is the pure domain entity for a hand.
// Persistence mapping lives in repository.handModel — no GORM tags here.
type Hand struct {
	ID        uuid.UUID
	HelmID    uuid.UUID
	Name      string
	Type      HandType
	Market    MarketType
	Status    string
	Symbols   StringSlice
	Strategy  StrategySpec
	Position  PositionConfig
	Risk      HandRiskConfig
	Futures   *FuturesConfig
	CreatedAt time.Time
	UpdatedAt time.Time
}

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
