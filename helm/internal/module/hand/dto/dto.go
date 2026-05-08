package dto

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
)

// ── Request DTOs ───────────────────────────────────────────────────────────

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

// UpdateHandReq allows patching Name, Position sizing, and Risk exit rules only.
// Symbols, Strategy, Type, and Market are immutable after creation.
type UpdateHandReq struct {
	Name     string             `json:"name" binding:"omitempty,min=1,max=128"`
	Position *PositionDTO       `json:"position"`
	Risk     *HandRiskConfigDTO `json:"risk"`
}

// StrategyDTO is the strategy spec sent to herald for signal generation.
// Exactly one mode must be set (checked by Validate):
//
//	Rhai mode  — Script non-empty: { "script": "let rsi = ind.RSI(14); ..." }
//
// Symmetric with HandSummary.Strategy — same flat format for input and output.
type StrategyDTO = domain.StrategySpec

// PositionDTO is the API representation of capital allocation and sizing config.
type PositionDTO struct {
	SizeMode         string  `json:"size_mode,omitempty" binding:"omitempty,oneof=fixed_fractional fixed_qty percent_equity volatility"`
	AllocatedCapital float64 `json:"allocated_capital,omitempty" binding:"omitempty,gte=0"`
	AllocatedPct     float64 `json:"allocated_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	UnitCapital      float64 `json:"unit_capital,omitempty" binding:"omitempty,gte=0"`
	UnitPct          float64 `json:"unit_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	FixedQty         float64 `json:"fixed_qty,omitempty" binding:"omitempty,gt=0"`
	MaxUnits         int     `json:"max_units,omitempty" binding:"omitempty,min=1"`
	Pyramid          bool    `json:"pyramid,omitempty"`
	RiskPerTradePct  float64 `json:"risk_per_trade_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	MaxPositionPct   float64 `json:"max_position_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
}

// HandRiskConfigDTO is the API representation of hand exit rules.
//
// Exit rules use almanac's ExitConfig format:
//
//	"exit": { "sl": 0.02 }                        → fixed 2% stop-loss
//	"exit": { "sl": "2*atr" }                     → 2× ATR stop-loss
//	"exit": { "sl": "1.5*atr(21)", "max_bars": 20 }
type HandRiskConfigDTO struct {
	Exit            *domain.ExitConfig `json:"exit,omitempty"`
	TrailingStopPct float64            `json:"trailing_stop_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
}

// FuturesDTO holds futures-specific parameters.
type FuturesDTO struct {
	Leverage   int    `json:"leverage" binding:"omitempty,min=1,max=125"`
	MarginType string `json:"margin_type" binding:"omitempty,oneof=isolated cross"`
}

// ConfigureStrategyReq is the legacy global-strategy config payload (engine.configure).
type ConfigureStrategyReq struct {
	Strategy string             `json:"strategy" binding:"required"`
	Params   map[string]float64 `json:"params"`
}

// ── Response DTOs ──────────────────────────────────────────────────────────

type HandActionResp struct {
	Status string `json:"status"`
	ID     string `json:"id"`
}

type ConfigureStrategyResp struct {
	Status   string `json:"status"`
	Strategy string `json:"strategy"`
}

// ── Conversions ────────────────────────────────────────────────────────────

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

func strategyToDomain(d StrategyDTO) domain.StrategySpec { return d }

func positionToDomain(dto PositionDTO) domain.PositionConfig {
	return domain.PositionConfig{
		SizeMode:         dto.SizeMode,
		AllocatedCapital: decimal.NewFromFloat(dto.AllocatedCapital),
		AllocatedPct:     dto.AllocatedPct,
		UnitCapital:      decimal.NewFromFloat(dto.UnitCapital),
		UnitPct:          dto.UnitPct,
		FixedQty:         decimal.NewFromFloat(dto.FixedQty),
		MaxUnits:         dto.MaxUnits,
		Pyramid:          dto.Pyramid,
		RiskPerTradePct:  dto.RiskPerTradePct,
		MaxPositionPct:   dto.MaxPositionPct,
	}
}

func riskToDomain(dto HandRiskConfigDTO) domain.HandRiskConfig {
	return domain.HandRiskConfig{
		Exit:            dto.Exit,
		TrailingStopPct: dto.TrailingStopPct,
	}
}
