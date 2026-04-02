package domain

import (
	"encoding/json"

	"github.com/google/uuid"
)

// BotType defines the operation mode of a bot.
type BotType string

const (
	BotTypeSignalFollower BotType = "signal_follower"
	BotTypeManual         BotType = "manual"
	BotTypeDCA            BotType = "dca"
	BotTypeGrid           BotType = "grid"
)

// MarketType defines which market the bot trades on.
type MarketType string

const (
	MarketTypeSpot    MarketType = "spot"
	MarketTypeFutures MarketType = "futures"
)

// Strategy is the pure signal/entry logic definition for a bot.
// It contains only what the signal-engine (herald) needs to generate signals.
//
// CEL tactic:   set Entry + Exit as expression strings; Name is ignored.
// Named tactic: set Name + Params; Entry/Exit are empty.
type Strategy struct {
	// CEL expressions — used when Entry is non-empty.
	// These are evaluated by the signal-engine (herald) via cel-interpreter.
	Entry string `json:"entry,omitempty"` // e.g. "rsi_14 < 30 && close > ema_50"
	// Tactic -> Execute Tactic
	Exit string `json:"exit,omitempty"` // e.g. "rsi_14 > 70 || close < ema_50"

	// Named strategy — used when Entry is empty.
	// Name must match a key in almanac's build_strategy() factory (e.g. "ma_crossover").
	Name   string         `json:"name,omitempty"`
	Params map[string]any `json:"params,omitempty"`

	// MinStrength is the minimum signal strength [0–1] for the bot to act.
	MinStrength float64 `json:"min_strength,omitempty"`
}

// StrategyName returns the strategy key sent to herald's build_strategy().
// CEL tactic → "cel"; named tactic → the Name field.
func (t Strategy) StrategyName() string {
	if t.Entry != "" {
		return "cel"
	}
	return t.Name
}

// ParamsJSON serializes the tactic params into the JSON string expected by
// herald's RegisterMsg.params_json field.
//
// CEL:   {"entry": "...", "exit": "..."}
// Named: the Params map as-is.
func (t Strategy) ParamsJSON() (string, error) {
	var v any
	if t.Entry != "" {
		v = map[string]string{"entry": t.Entry, "exit": t.Exit}
	} else {
		v = t.Params
	}
	b, err := json.Marshal(v)
	return string(b), err
}

// BotRiskConfig defines per-bot position sizing, risk limits, and exit levels.
// Distinct from orchestrator-level RiskConfig (portfolio-wide).
type BotRiskConfig struct {
	// Position sizing
	SizeMode        string  `json:"size_mode,omitempty"`
	RiskPerTradePct float64 `json:"risk_per_trade_pct,omitempty"`
	MaxPositionPct  float64 `json:"max_position_pct,omitempty"`
	FixedQty        float64 `json:"fixed_qty,omitempty"`

	// ATR-based exits (preferred when ATR is available)
	StopLossATRMult   float64 `json:"stop_loss_atr_mult,omitempty"`
	TakeProfitATRMult float64 `json:"take_profit_atr_mult,omitempty"` // 0 = use 2x SL rule

	// Percentage-based exits (fallback when ATR = 0)
	StopLossPct     float64 `json:"stop_loss_pct,omitempty"`     // e.g. 0.02 = 2%
	TakeProfitPct   float64 `json:"take_profit_pct,omitempty"`   // e.g. 0.04 = 4%
	TrailingStopPct float64 `json:"trailing_stop_pct,omitempty"` // trailing stop as % of entry (0 = disabled)
	MaxBarsHeld     int     `json:"max_bars_held,omitempty"`     // time-stop: close after N bars (0 = disabled)
}

// FuturesConfig holds futures-specific trading parameters.
// Only meaningful when BotConfig.Market == MarketTypeFutures.
type FuturesConfig struct {
	Leverage   int    `json:"leverage"`    // e.g. 10 for 10x; 1 = no leverage
	MarginType string `json:"margin_type"` // "isolated" | "cross"
}

// BotConfig is the complete, structured configuration for a trading bot instance.
// OrchestratorID links the bot to its parent orchestrator (and thus its account + exchange).
type BotConfig struct {
	Name           string         `json:"name"`
	Type           BotType        `json:"type"`
	Market         MarketType     `json:"market"`
	OrchestratorID uuid.UUID      `json:"orchestrator_id"`
	Symbols        []string       `json:"symbols"`
	Tactic         Strategy       `json:"tactic"`
	Risk           BotRiskConfig  `json:"risk"`
	Futures        *FuturesConfig `json:"futures,omitempty"` // non-nil only when Market = futures
}

// Defaults fills in zero-value fields with sensible defaults.
func (c *BotConfig) Defaults() {
	if c.Type == "" {
		c.Type = BotTypeSignalFollower
	}
	if c.Market == "" {
		c.Market = MarketTypeSpot
	}
	if c.Market == MarketTypeFutures && c.Futures == nil {
		c.Futures = &FuturesConfig{Leverage: 1, MarginType: "isolated"}
	}
	if c.Risk.SizeMode == "" {
		c.Risk.SizeMode = "fixed_fractional"
	}
	if c.Risk.RiskPerTradePct == 0 {
		c.Risk.RiskPerTradePct = 0.01
	}
	if c.Risk.MaxPositionPct == 0 {
		c.Risk.MaxPositionPct = 0.20
	}
	if c.Risk.StopLossATRMult == 0 {
		c.Risk.StopLossATRMult = 2.0
	}
	if c.Tactic.MinStrength == 0 {
		c.Tactic.MinStrength = 0.3
	}
}
