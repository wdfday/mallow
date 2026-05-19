package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Hand is the pure domain entity for a hand.
// Persistence mapping lives in repository.handModel — no GORM tags here.
type Hand struct {
	ID       uuid.UUID
	HelmID   uuid.UUID
	Name     string
	Type     HandType
	Market   MarketType
	Status   HandStatus
	Symbols  StringSlice
	Strategy StrategySpec
	Position PositionConfig
	Risk     HandRiskConfig
	Futures  *FuturesConfig

	// AllocatedCapital is the hand's fixed capital budget (quote currency, e.g. USDT).
	// Zero = hand draws from full helm equity without isolation.
	AllocatedCapital decimal.Decimal

	// SignalTTLSec is the max age of a signal before it is silently discarded.
	// 0 = default (10s). Negative = disable.
	SignalTTLSec int

	// OrderType is the default entry order type. "market" or "limit".
	OrderType OrderType

	// LimitTimeoutSec cancels an unfilled limit order after N seconds. 0 = no timeout.
	// Only meaningful when OrderType == "limit".
	LimitTimeoutSec int

	// LimitFallback controls what happens after a timed-out limit is cancelled.
	// "cancel" = do nothing. "market" = re-place as market order.
	LimitFallback LimitFallback

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
	b.AllocatedCapital = cfg.AllocatedCapital
	b.SignalTTLSec = cfg.SignalTTLSec
	b.OrderType = cfg.OrderType
	b.LimitTimeoutSec = cfg.LimitTimeoutSec
	b.LimitFallback = cfg.LimitFallback
}
