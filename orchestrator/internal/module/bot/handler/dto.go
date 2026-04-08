package handler

import (
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"orchestrator/internal/module/bot/domain"
)

// ── Request DTOs ───────────────────────────────────────────────────────────

type CreateBotReq struct {
	Name           string           `json:"name" binding:"required,min=1,max=128"`
	Type           domain.BotType   `json:"type" binding:"omitempty,oneof=signal_follower manual dca grid"`
	OrchestratorID uuid.UUID        `json:"orchestrator_id"`
	AccountID      uuid.UUID        `json:"account_id"`
	Symbols        []string         `json:"symbols" binding:"required,min=1"`
	Tactic         TacticDTO        `json:"tactic" binding:"required"`
	Risk           BotRiskConfigDTO `json:"risk"`
}

type UpdateBotReq struct {
	Name    string            `json:"name" binding:"omitempty,min=1,max=128"`
	Symbols []string          `json:"symbols" binding:"omitempty,min=1"`
	Tactic  *TacticDTO        `json:"tactic"`
	Risk    *BotRiskConfigDTO `json:"risk"`
}

// TacticDTO is the API representation of a bot tactic (pure signal/entry logic).
//
// CEL tactic (preferred):
//
//	{ "entry": "rsi_14 < 30 && close > ema_50", "exit": "rsi_14 > 70" }
//
// Named tactic:
//
//	{ "name": "ma_crossover", "params": { "fast": 20, "slow": 50 } }
type TacticDTO struct {
	// CEL expressions
	Entry string `json:"entry,omitempty"`
	Exit  string `json:"exit,omitempty"`

	// Named strategy
	Name   string         `json:"name,omitempty"`
	Params map[string]any `json:"params,omitempty"`

	MinStrength float64 `json:"min_strength,omitempty"`
}

type BotRiskConfigDTO struct {
	// Sizing
	SizeMode        string  `json:"size_mode,omitempty" binding:"omitempty,oneof=fixed_fractional fixed_qty percent_equity volatility"`
	RiskPerTradePct float64 `json:"risk_per_trade_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	MaxPositionPct  float64 `json:"max_position_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	FixedQty        float64 `json:"fixed_qty,omitempty" binding:"omitempty,gt=0"`

	// ATR-based exits
	StopLossATRMult   float64 `json:"stop_loss_atr_mult,omitempty" binding:"omitempty,gt=0"`
	TakeProfitATRMult float64 `json:"take_profit_atr_mult,omitempty" binding:"omitempty,gt=0"`

	// Percentage-based exits
	StopLossPct     float64 `json:"stop_loss_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	TakeProfitPct   float64 `json:"take_profit_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	TrailingStopPct float64 `json:"trailing_stop_pct,omitempty" binding:"omitempty,gt=0,lte=1"`
	MaxBarsHeld     int     `json:"max_bars_held,omitempty" binding:"omitempty,gt=0"`
}

// ConfigureStrategyReq is the legacy global-strategy config payload (engine.configure).
// Params must be float64 values to match ConfigMsg proto (map<string, double>).
type ConfigureStrategyReq struct {
	Strategy string             `json:"strategy" binding:"required"`
	Params   map[string]float64 `json:"params"`
}

// ── Response DTOs ──────────────────────────────────────────────────────────

type BotActionResp struct {
	Status string `json:"status"`
	ID     string `json:"id"`
}

type ConfigureStrategyResp struct {
	Status   string `json:"status"`
	Strategy string `json:"strategy"`
}

// ── Conversions ────────────────────────────────────────────────────────────

func (r CreateBotReq) ToDomain() domain.BotConfig {
	return domain.BotConfig{
		Name:           r.Name,
		Type:           r.Type,
		OrchestratorID: r.OrchestratorID,
		Symbols:        r.Symbols,
		Tactic:         tacticToDomain(r.Tactic),
		Risk:           riskToDomain(r.Risk),
	}
}

func (r UpdateBotReq) ToDomain(existing domain.BotConfig) domain.BotConfig {
	cfg := existing
	if r.Name != "" {
		cfg.Name = r.Name
	}
	if len(r.Symbols) > 0 {
		cfg.Symbols = r.Symbols
	}
	if r.Tactic != nil {
		cfg.Tactic = tacticToDomain(*r.Tactic)
	}
	if r.Risk != nil {
		cfg.Risk = riskToDomain(*r.Risk)
	}
	return cfg
}

func tacticToDomain(dto TacticDTO) domain.Strategy {
	return domain.Strategy{
		Entry:       dto.Entry,
		Exit:        dto.Exit,
		Name:        dto.Name,
		Params:      dto.Params,
		MinStrength: dto.MinStrength,
	}
}

func riskToDomain(dto BotRiskConfigDTO) domain.BotRiskConfig {
	return domain.BotRiskConfig{
		SizeMode:          dto.SizeMode,
		RiskPerTradePct:   dto.RiskPerTradePct,
		MaxPositionPct:    dto.MaxPositionPct,
		FixedQty:          decimal.NewFromFloat(dto.FixedQty),
		StopLossATRMult:   dto.StopLossATRMult,
		TakeProfitATRMult: dto.TakeProfitATRMult,
		StopLossPct:       dto.StopLossPct,
		TakeProfitPct:     dto.TakeProfitPct,
		TrailingStopPct:   dto.TrailingStopPct,
		MaxBarsHeld:       dto.MaxBarsHeld,
	}
}
