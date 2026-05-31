package dto

import (
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
)

// StrategyDTO is the strategy spec sent to herald for signal generation.
// Symmetric with HandSummary.Strategy — same flat format for input and output.
//
//	Script mode — Script non-empty: { "script": "let rsi = ind.RSI(14); ..." }
type StrategyDTO = domain.StrategySpec

// PositionDTO is the API representation of per-trade sizing config.
// AllocatedCapital and SignalTTLSec live on the parent request (CreateHandReq / UpdateHandReq),
// not here — they are first-class columns, not part of the position JSONB.
type PositionDTO struct {
	SizeMode      domain.SizeMode `json:"size_mode,omitempty" binding:"omitempty,oneof=fixed_fractional fixed_qty quote_qty percent_equity volatility"`
	UnitPct       float64         `json:"unit_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	FixedQty      float64         `json:"fixed_qty,omitempty" binding:"omitempty,gt=0"`
	FixedQuoteQty float64         `json:"fixed_quote_qty,omitempty" binding:"omitempty,gt=0"`
	MaxUnits      int             `json:"max_units,omitempty" binding:"omitempty,min=1"`
	Pyramid       bool            `json:"pyramid,omitempty"`
	// RiskPerTradePct: fraction of equity risked per trade (Ralph Vince). Capped at 0.1
	// (10%) — risking more than that per trade is reckless, one stop-out near-wipes the book.
	RiskPerTradePct float64 `json:"risk_per_trade_pct,omitempty" binding:"omitempty,gt=0,lte=0.1"`
	MaxPositionPct  float64 `json:"max_position_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
}

// HandGuardConfigDTO is the API representation of per-hand edge-degradation settings.
// All fields are optional; zero means disabled.
type HandGuardConfigDTO struct {
	WindowTrades     int     `json:"window_trades,omitempty"      binding:"omitempty,min=1,max=1000"`
	MaxTotalLossPct  float64 `json:"max_total_loss_pct,omitempty"  binding:"omitempty,gt=0,lte=1"`
	MaxAvgLossPct    float64 `json:"max_avg_loss_pct,omitempty"    binding:"omitempty,gt=0,lte=1"`
	MaxSingleLossPct float64 `json:"max_single_loss_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	MaxConsecLoss    int     `json:"max_consec_loss,omitempty"     binding:"omitempty,min=1,max=100"`
}

// FuturesDTO holds futures-specific parameters.
type FuturesDTO struct {
	Leverage   int               `json:"leverage,omitempty" binding:"omitempty,min=1,max=125"`
	MarginType domain.MarginType `json:"margin_type,omitempty" binding:"omitempty,oneof=isolated cross"`
}

// ── conversions ────────────────────────────────────────────────────────────

func strategyToDomain(d StrategyDTO) domain.StrategySpec { return d }

func positionToDomain(d PositionDTO) domain.PositionConfig {
	return domain.PositionConfig{
		SizeMode:        d.SizeMode,
		UnitPct:         d.UnitPct,
		FixedQty:        decimal.NewFromFloat(d.FixedQty),
		FixedQuoteQty:   decimal.NewFromFloat(d.FixedQuoteQty),
		MaxUnits:        d.MaxUnits,
		Pyramid:         d.Pyramid,
		RiskPerTradePct: d.RiskPerTradePct,
		MaxPositionPct:  d.MaxPositionPct,
	}
}

func guardToDomain(d HandGuardConfigDTO) domain.HandGuardConfig {
	return domain.HandGuardConfig{
		WindowTrades:     d.WindowTrades,
		MaxTotalLossPct:  d.MaxTotalLossPct,
		MaxAvgLossPct:    d.MaxAvgLossPct,
		MaxSingleLossPct: d.MaxSingleLossPct,
		MaxConsecLoss:    d.MaxConsecLoss,
	}
}
