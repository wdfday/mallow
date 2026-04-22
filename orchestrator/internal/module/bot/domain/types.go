package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
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

// ── StrategyConfig ────────────────────────────────────────────────────────────

// StrategyConfig is the signal-generation config sent to herald.
//
// CEL mode  (Entry non-empty): entry/exit are cel-go expressions.
// Named mode (Entry empty):    Name must match a key in almanac's build_strategy().
type StrategyConfig struct {
	// CEL expressions
	Entry string `json:"entry,omitempty"` // e.g. "rsi_14 < 30 && close > ema_50"
	Exit  string `json:"exit,omitempty"`  // e.g. "rsi_14 > 70 || close < ema_50"

	// Named strategy
	Name   string         `json:"name,omitempty"`
	Params map[string]any `json:"params,omitempty"`

	// Signal strength filter [0–1]
	MinStrength float64 `json:"min_strength,omitempty"`
}

// Key returns the strategy key for herald's build_strategy().
func (s StrategyConfig) Key() string {
	if s.Entry != "" {
		return "cel"
	}
	return s.Name
}

// ParamsJSON serialises strategy params for herald's RegisterMsg.params_json.
func (s StrategyConfig) ParamsJSON() (string, error) {
	var v any
	if s.Entry != "" {
		v = map[string]string{"entry": s.Entry, "exit": s.Exit}
	} else {
		v = s.Params
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func (s StrategyConfig) Value() (driver.Value, error) { return jsonValue(s) }
func (s *StrategyConfig) Scan(src any) error          { return jsonScan(src, s) }

// ── PositionConfig ────────────────────────────────────────────────────────────

// PositionConfig controls capital allocation and per-trade sizing.
// Maps directly to BacktestRequest sizing fields.
type PositionConfig struct {
	// Capital slice for this bot.
	// AllocatedCapital (fixed USDT) takes priority over AllocatedPct.
	AllocatedCapital decimal.Decimal `json:"allocated_capital,omitempty"`
	AllocatedPct     float64         `json:"allocated_pct,omitempty"` // 0.20 = 20%

	// Per-trade unit.
	// UnitCapital (fixed USDT) takes priority over UnitPct.
	UnitCapital decimal.Decimal `json:"unit_capital,omitempty"`
	UnitPct     float64         `json:"unit_pct,omitempty"` // 0.10 = 10% of allocated

	// Fixed qty mode — overrides USD/pct sizing.
	FixedQty decimal.Decimal `json:"fixed_qty,omitempty"`

	// Concurrent position cap.
	MaxPositions int `json:"max_positions,omitempty"`

	// Sizing algorithm.
	SizeMode        string  `json:"size_mode,omitempty"`          // fixed_fractional|fixed_qty|percent_equity|volatility
	RiskPerTradePct float64 `json:"risk_per_trade_pct,omitempty"` // for volatility mode
	MaxPositionPct  float64 `json:"max_position_pct,omitempty"`   // legacy fallback
}

func (p PositionConfig) Value() (driver.Value, error) { return jsonValue(p) }
func (p *PositionConfig) Scan(src any) error          { return jsonScan(src, p) }

// ── BotRiskConfig ─────────────────────────────────────────────────────────────

// BotRiskConfig holds exit rules only.
// Sizing lives in PositionConfig; portfolio-level risk lives in OrchestratorConfig.
type BotRiskConfig struct {
	// Exit rules — mirrors almanac's ExitConfig for backtest/live parity.
	// sl/tp: fixed fraction (0.05) or ATR expression ("2*atr", "1.5*atr(21)").
	Exit *ExitConfig `json:"exit,omitempty"`

	// TrailingStopPct is live-only (not in backtest ExitConfig).
	TrailingStopPct float64 `json:"trailing_stop_pct,omitempty"`
}

func (r BotRiskConfig) Value() (driver.Value, error) { return jsonValue(r) }
func (r *BotRiskConfig) Scan(src any) error          { return jsonScan(src, r) }

// ── FuturesConfig ─────────────────────────────────────────────────────────────

// FuturesConfig holds futures-specific parameters.
// Only meaningful when BotInstance.Market == MarketTypeFutures.
type FuturesConfig struct {
	Leverage   int    `json:"leverage"`    // e.g. 10 for 10x; 1 = no leverage
	MarginType string `json:"margin_type"` // "isolated" | "cross"
}

func (f FuturesConfig) Value() (driver.Value, error) { return jsonValue(f) }
func (f *FuturesConfig) Scan(src any) error          { return jsonScan(src, f) }

// ── StringSlice ───────────────────────────────────────────────────────────────

// StringSlice is a []string that serialises as a JSONB array.
type StringSlice []string

func (s StringSlice) Value() (driver.Value, error) { return jsonValue(s) }
func (s *StringSlice) Scan(src any) error          { return jsonScan(src, s) }

// ── BotConfig ─────────────────────────────────────────────────────────────────

// BotConfig is the create/update input. Not persisted directly —
// the service maps it onto a BotInstance.
type BotConfig struct {
	Name           string
	Type           BotType
	Market         MarketType
	OrchestratorID uuid.UUID
	Symbols        []string
	Strategy       StrategyConfig
	Position       PositionConfig
	Risk           BotRiskConfig
	Futures        *FuturesConfig
}

// Defaults fills zero-value fields with sensible values.
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
	if c.Position.SizeMode == "" {
		c.Position.SizeMode = "fixed_fractional"
	}
	if c.Position.RiskPerTradePct == 0 {
		c.Position.RiskPerTradePct = 0.01
	}
	if c.Position.MaxPositionPct == 0 {
		c.Position.MaxPositionPct = 0.20
	}
	if c.Position.UnitCapital.IsZero() && c.Position.UnitPct == 0 {
		c.Position.UnitPct = 0.10
	}
	if c.Position.MaxPositions == 0 {
		c.Position.MaxPositions = 1
	}
	if c.Risk.Exit == nil {
		c.Risk.Exit = &ExitConfig{SL: ExitLevelATR(2.0)}
	}
	if c.Strategy.MinStrength == 0 {
		c.Strategy.MinStrength = 0.3
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func jsonValue(v any) (driver.Value, error) {
	b, err := json.Marshal(v)
	return b, err
}

func jsonScan(src any, dst any) error {
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	case nil:
		return nil
	default:
		return fmt.Errorf("jsonScan: unsupported type %T", src)
	}
	return json.Unmarshal(b, dst)
}
