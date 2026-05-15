package dto

import (
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
)

// StrategyDTO is the strategy spec sent to herald for signal generation.
// Symmetric with HandSummary.Strategy — same flat format for input and output.
//
//	Rhai mode — Script non-empty: { "script": "let rsi = ind.RSI(14); ..." }
type StrategyDTO = domain.StrategySpec

// PositionDTO is the API representation of capital allocation and sizing config.
type PositionDTO struct {
	SizeMode         string  `json:"size_mode,omitempty" binding:"omitempty,oneof=fixed_fractional fixed_qty quote_qty percent_equity volatility"`
	AllocatedCapital float64 `json:"allocated_capital,omitempty" binding:"omitempty,gte=0"`
	AllocatedPct     float64 `json:"allocated_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	UnitCapital      float64 `json:"unit_capital,omitempty" binding:"omitempty,gte=0"`
	UnitPct          float64 `json:"unit_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	FixedQty         float64 `json:"fixed_qty,omitempty" binding:"omitempty,gt=0"`
	FixedQuoteQty    float64 `json:"fixed_quote_qty,omitempty" binding:"omitempty,gt=0"`
	MaxUnits         int     `json:"max_units,omitempty" binding:"omitempty,min=1"`
	Pyramid          bool    `json:"pyramid,omitempty"`
	RiskPerTradePct  float64 `json:"risk_per_trade_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	MaxPositionPct   float64 `json:"max_position_pct,omitempty" binding:"omitempty,gt=0,lte=1"`

	// Limit order lifecycle.
	LimitTimeoutSec int    `json:"limit_timeout_sec,omitempty" binding:"omitempty,min=5,max=3600"`
	LimitFallback   string `json:"limit_fallback,omitempty" binding:"omitempty,oneof=cancel market"`
}

// HandRiskConfigDTO is the API representation of per-hand risk settings.
type HandRiskConfigDTO struct {
	// SignalTTLSec: 0 = use default (10s), -1 = disable TTL check, >0 = custom TTL in seconds.
	SignalTTLSec int `json:"signal_ttl_sec,omitempty" binding:"omitempty,min=-1,max=3600"`
}

// FuturesDTO holds futures-specific parameters.
type FuturesDTO struct {
	Leverage   int    `json:"leverage,omitempty" binding:"omitempty,min=1,max=125"`
	MarginType string `json:"margin_type,omitempty" binding:"omitempty,oneof=isolated cross"`
}

// ── conversions ────────────────────────────────────────────────────────────

func strategyToDomain(d StrategyDTO) domain.StrategySpec { return d }

func positionToDomain(d PositionDTO) domain.PositionConfig {
	return domain.PositionConfig{
		SizeMode:         d.SizeMode,
		AllocatedCapital: decimal.NewFromFloat(d.AllocatedCapital),
		AllocatedPct:     d.AllocatedPct,
		UnitCapital:      decimal.NewFromFloat(d.UnitCapital),
		UnitPct:          d.UnitPct,
		FixedQty:         decimal.NewFromFloat(d.FixedQty),
		FixedQuoteQty:    decimal.NewFromFloat(d.FixedQuoteQty),
		MaxUnits:         d.MaxUnits,
		Pyramid:          d.Pyramid,
		RiskPerTradePct:  d.RiskPerTradePct,
		MaxPositionPct:   d.MaxPositionPct,
		LimitTimeoutSec:  d.LimitTimeoutSec,
		LimitFallback:    d.LimitFallback,
	}
}

func riskToDomain(d HandRiskConfigDTO) domain.HandRiskConfig {
	return domain.HandRiskConfig{SignalTTLSec: d.SignalTTLSec}
}
