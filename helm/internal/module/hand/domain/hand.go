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
	Guard    HandGuardConfig
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

	// FinalMetrics is populated on Kill/Release and persisted to DB.
	// Nil for active hands; non-nil for terminal hands (killed/released).
	FinalMetrics *HandMetricsView

	CreatedAt time.Time
	UpdatedAt time.Time
}

// SummaryFromDB builds a HandSummary from the domain entity alone (no runner).
// Used for terminal hands that are no longer in memory.
func (h *Hand) SummaryFromDB() HandSummary {
	var metrics HandMetricsView
	if h.FinalMetrics != nil {
		metrics = *h.FinalMetrics
	}
	return HandSummary{
		ID:               h.ID,
		Name:             h.Name,
		Type:             h.Type,
		Market:           h.Market,
		HelmID:           h.HelmID,
		Strategy:         h.Strategy,
		Position:         h.Position,
		Guard:            h.Guard,
		Symbols:          []string(h.Symbols),
		Status:           h.Status,
		Running:          false,
		AllocatedCapital: h.AllocatedCapital,
		Metrics:          metrics,
		CreatedAt:        h.CreatedAt,
	}
}

// ApplyConfig copies mutable config fields from a HandConfig onto the hand.
// Identity fields (ID, HelmID, Status, CreatedAt) must be set by the caller.
func (b *Hand) ApplyConfig(cfg HandConfig) {
	b.Name = cfg.Name
	b.Type = cfg.Type
	b.Market = cfg.Market
	b.Symbols = StringSlice(cfg.Symbols)
	b.Strategy = cfg.Strategy
	b.Position = cfg.Position
	b.Guard = cfg.Guard
	b.Futures = cfg.Futures
	b.AllocatedCapital = cfg.AllocatedCapital
	b.SignalTTLSec = cfg.SignalTTLSec
	b.OrderType = cfg.OrderType
	b.LimitTimeoutSec = cfg.LimitTimeoutSec
	b.LimitFallback = cfg.LimitFallback
}
