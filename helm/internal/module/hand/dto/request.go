package dto

import (
	"github.com/google/uuid"

	"mallow/helm/internal/module/hand/domain"
)

// CreateHandReq is the full hand creation payload.
type CreateHandReq struct {
	Name      string            `json:"name" binding:"required,min=1,max=128"`
	Type      domain.HandType   `json:"type" binding:"omitempty,oneof=signal_follower manual dca grid"`
	Market    domain.MarketType `json:"market" binding:"omitempty,oneof=spot futures"`
	HelmID    uuid.UUID         `json:"helm_id"`
	AccountID uuid.UUID         `json:"account_id"`
	Symbols   []string          `json:"symbols" binding:"required,min=1"`
	Strategy  StrategyDTO       `json:"strategy" binding:"required"`
	Position  PositionDTO       `json:"position"`
	Risk      HandRiskConfigDTO `json:"risk"`
	Futures   *FuturesDTO       `json:"futures"`
}

func (r CreateHandReq) ToDomain() domain.HandConfig {
	cfg := domain.HandConfig{
		Name:     r.Name,
		Type:     r.Type,
		Market:   r.Market,
		HelmID:   r.HelmID,
		Symbols:  r.Symbols,
		Strategy: strategyToDomain(r.Strategy),
		Position: positionToDomain(r.Position),
		Risk:     riskToDomain(r.Risk),
	}
	if r.Futures != nil {
		cfg.Futures = &domain.FuturesConfig{
			Leverage:   r.Futures.Leverage,
			MarginType: r.Futures.MarginType,
		}
	}
	return cfg
}

// UpdateHandReq allows patching Name, Position sizing, and Risk exit rules only.
// Symbols, Strategy, Type, and Market are immutable after creation.
type UpdateHandReq struct {
	Name     string             `json:"name" binding:"omitempty,min=1,max=128"`
	Position *PositionDTO       `json:"position"`
	Risk     *HandRiskConfigDTO `json:"risk"`
}

func (r UpdateHandReq) ToDomain() domain.HandConfig {
	cfg := domain.HandConfig{Name: r.Name}
	if r.Position != nil {
		cfg.Position = positionToDomain(*r.Position)
	}
	if r.Risk != nil {
		cfg.Risk = riskToDomain(*r.Risk)
	}
	return cfg
}

// ConfigureStrategyReq is the legacy global-strategy config payload (engine.configure).
type ConfigureStrategyReq struct {
	Strategy string             `json:"strategy" binding:"required"`
	Params   map[string]float64 `json:"params"`
}
