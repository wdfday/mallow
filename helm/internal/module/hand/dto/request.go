package dto

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
)

// CreateHandReq is the full hand creation payload.
type CreateHandReq struct {
	Name      string            `json:"name" binding:"required,min=1,max=128"`
	Type      domain.HandType   `json:"type" binding:"omitempty,oneof=signal_follower"`
	Market    domain.MarketType `json:"market" binding:"omitempty,oneof=spot futures"`
	HelmID    uuid.UUID         `json:"helm_id"`
	AccountID uuid.UUID         `json:"account_id"`
	// Only symbols[0] is dispatched to herald — single-symbol per hand. Multi-symbol deferred.
	Symbols  []string    `json:"symbols" binding:"required,min=1,max=1"`
	Strategy StrategyDTO `json:"strategy" binding:"required"`
	// AllocatedCapital is the hand's fixed capital budget (quote currency, e.g. USDT).
	// Must be greater than zero.
	AllocatedCapital float64 `json:"allocated_capital" binding:"required,gt=0"`
	// SignalTTLSec: max age of a signal before discard. 0 = default (10s), -1 = disable.
	SignalTTLSec int `json:"signal_ttl_sec,omitempty" binding:"omitempty,min=-1,max=3600"`
	// OrderType: default entry order type. "market" (default) or "limit".
	OrderType       domain.OrderType     `json:"order_type,omitempty" binding:"omitempty,oneof=market limit"`
	LimitTimeoutSec int                  `json:"limit_timeout_sec,omitempty" binding:"omitempty,min=5,max=3600"`
	LimitFallback   domain.LimitFallback `json:"limit_fallback,omitempty" binding:"omitempty,oneof=cancel market"`
	Position        PositionDTO          `json:"position"`
	Guard           HandGuardConfigDTO   `json:"risk"`
	Futures         *FuturesDTO          `json:"futures"`
}

func (r CreateHandReq) ToDomain() domain.HandConfig {
	cfg := domain.HandConfig{
		Name:             r.Name,
		Type:             r.Type,
		Market:           r.Market,
		HelmID:           r.HelmID,
		Symbols:          r.Symbols,
		Strategy:         strategyToDomain(r.Strategy),
		Position:         positionToDomain(r.Position),
		Guard:            guardToDomain(r.Guard),
		AllocatedCapital: decimal.NewFromFloat(r.AllocatedCapital),
		SignalTTLSec:     r.SignalTTLSec,
		OrderType:        r.OrderType,
		LimitTimeoutSec:  r.LimitTimeoutSec,
		LimitFallback:    r.LimitFallback,
	}
	if r.Futures != nil {
		cfg.Futures = &domain.FuturesConfig{
			Leverage:   r.Futures.Leverage,
			MarginType: r.Futures.MarginType,
		}
	}
	return cfg
}

// UpdateHandReq allows patching Name, Position sizing, and Guard.
// Immutable after creation: symbols, strategy, type, market, allocated_capital (use /allocate-capital).
type UpdateHandReq struct {
	Name     string              `json:"name" binding:"omitempty,min=1,max=128"`
	Position *PositionDTO        `json:"position"`
	Guard    *HandGuardConfigDTO `json:"risk"`
}

func (r UpdateHandReq) ToDomain() domain.HandConfig {
	cfg := domain.HandConfig{Name: r.Name}
	if r.Position != nil {
		cfg.Position = positionToDomain(*r.Position)
	}
	if r.Guard != nil {
		cfg.Guard = guardToDomain(*r.Guard)
	}
	return cfg
}

// ConfigureStrategyReq is the legacy global-strategy config payload (engine.configure).
type ConfigureStrategyReq struct {
	Strategy string             `json:"strategy" binding:"required"`
	Params   map[string]float64 `json:"params"`
}

// AllocateCapitalReq is the payload to increase or decrease a hand's capital budget.
type AllocateCapitalReq struct {
	Amount float64 `json:"amount" binding:"required"`
}
