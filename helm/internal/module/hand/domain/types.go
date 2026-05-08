package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// HandType defines the operation mode of a hand.
type HandType string

const (
	HandTypeSignalFollower HandType = "signal_follower"
	HandTypeManual         HandType = "manual"
	HandTypeDCA            HandType = "dca"
	HandTypeGrid           HandType = "grid"
)

// MarketType defines which market the hand trades on.
type MarketType string

const (
	MarketTypeSpot    MarketType = "spot"
	MarketTypeFutures MarketType = "futures"
)

// ── StrategySpec ──────────────────────────────────────────────────────────────

// StrategySpec is the signal-generation spec sent to herald.
// Script is the full Rhai source sent as params["script"] to herald's
// rhai_strategy; indicators declared via ind.TYPE(period) syntax.
type StrategySpec struct {
	// Rhai script — full Rhai source code (required)
	Script string `json:"script"`

	// Timeframe the script operates on (e.g. "M1", "M5", "H1", "D1").
	// Defaults to "M1" via HandConfig.Defaults().
	Timeframe string `json:"timeframe,omitempty"`

	// CandleType controls the bar transform applied before indicators:
	//   ""              / "raw"         — standard OHLCV (default)
	//   "heiken_ashi"   / "ha"          — Heikin Ashi
	//   "smooth_ha"                     — EMA-smoothed Heikin Ashi (see SmoothPeriod)
	CandleType string `json:"candle_type,omitempty"`

	// SmoothPeriod is the EMA period for "smooth_ha" mode (default 3, min 2).
	// Ignored for other candle types.
	SmoothPeriod int `json:"smooth_period,omitempty"`

	// Signal strength filter [0–1]
	MinStrength float64 `json:"min_strength,omitempty"`
}

// validTimeframes is the set of timeframe strings accepted by herald.
var validTimeframes = map[string]bool{
	"M1": true, "M5": true, "M15": true, "M30": true,
	"H1": true, "H4": true, "D1": true, "W1": true,
}

// validCandleTypes is the set of candle_type strings accepted by rhai_strategy.
var validCandleTypes = map[string]bool{
	"": true, "raw": true, "heiken_ashi": true, "ha": true, "smooth_ha": true,
}

// Validate checks that Script is non-empty and optional fields are valid.
func (s StrategySpec) Validate() error {
	if s.Script == "" {
		return fmt.Errorf("strategy: script is required")
	}
	if s.Timeframe != "" && !validTimeframes[s.Timeframe] {
		return fmt.Errorf("strategy: invalid timeframe %q (valid: M1 M5 M15 M30 H1 H4 D1 W1)", s.Timeframe)
	}
	if !validCandleTypes[s.CandleType] {
		return fmt.Errorf("strategy: invalid candle_type %q (valid: raw heiken_ashi ha smooth_ha)", s.CandleType)
	}
	return nil
}

func (s StrategySpec) Value() (driver.Value, error) { return jsonValue(s) }
func (s *StrategySpec) Scan(src any) error          { return jsonScan(src, s) }

// ── PositionConfig ────────────────────────────────────────────────────────────

// PositionConfig controls capital allocation, per-trade sizing, and scaling behaviour.
// Maps directly to BacktestRequest sizing fields.
type PositionConfig struct {
	// Capital slice for this hand.
	// AllocatedCapital (fixed USDT) takes priority over AllocatedPct.
	AllocatedCapital decimal.Decimal `json:"allocated_capital,omitempty"`
	AllocatedPct     float64         `json:"allocated_pct,omitempty"` // 0.20 = 20%

	// Per-trade unit.
	// UnitCapital (fixed USDT) takes priority over UnitPct.
	UnitCapital decimal.Decimal `json:"unit_capital,omitempty"`
	UnitPct     float64         `json:"unit_pct,omitempty"` // 0.10 = 10% of allocated

	// Fixed qty mode — overrides USD/pct sizing.
	FixedQty decimal.Decimal `json:"fixed_qty,omitempty"`

	// MaxUnits is the maximum number of concurrent entry legs.
	// 1 = no scaling (default). Each new entry signal while at max is rejected.
	// Overridden downward by helm-level RiskConfig.MaxUnitsPerHand if set.
	MaxUnits int `json:"max_units,omitempty"`

	// Pyramid controls how additional entry signals are handled while a position is open.
	// true  → merge into the existing leg: qty accumulates, avg_entry recalculated,
	//         SL/TP replaced with values from the latest signal.
	// false → open a new independent leg with its own SL/TP (up to MaxUnits).
	Pyramid bool `json:"pyramid,omitempty"`

	// Sizing algorithm.
	SizeMode        string  `json:"size_mode,omitempty"`          // fixed_fractional|fixed_qty|percent_equity|volatility
	RiskPerTradePct float64 `json:"risk_per_trade_pct,omitempty"` // for volatility mode
	MaxPositionPct  float64 `json:"max_position_pct,omitempty"`   // legacy fallback
}

func (p PositionConfig) Value() (driver.Value, error) { return jsonValue(p) }
func (p *PositionConfig) Scan(src any) error          { return jsonScan(src, p) }

// ── HandRiskConfig ─────────────────────────────────────────────────────────────

// HandRiskConfig holds exit rules only.
// Sizing lives in PositionConfig; portfolio-level risk lives in HelmConfig.
type HandRiskConfig struct {
	// Exit rules — mirrors almanac's ExitConfig for backtest/live parity.
	// sl/tp: fixed fraction (0.05) or ATR expression ("2*atr", "1.5*atr(21)").
	Exit *ExitConfig `json:"exit,omitempty"`

	// TrailingStopPct is live-only (not in backtest ExitConfig).
	TrailingStopPct float64 `json:"trailing_stop_pct,omitempty"`
}

func (r HandRiskConfig) Value() (driver.Value, error) { return jsonValue(r) }
func (r *HandRiskConfig) Scan(src any) error          { return jsonScan(src, r) }

// ── FuturesConfig ─────────────────────────────────────────────────────────────

// FuturesConfig holds futures-specific parameters.
// Only meaningful when Hand.Market == MarketTypeFutures.
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

// ── HandConfig ─────────────────────────────────────────────────────────────────

// HandConfig is the create/update input. Not persisted directly —
// the service maps it onto a Hand.
type HandConfig struct {
	Name     string
	Type     HandType
	Market   MarketType
	HelmID   uuid.UUID
	Symbols  []string
	Strategy StrategySpec
	Position PositionConfig
	Risk     HandRiskConfig
	Futures  *FuturesConfig
}

// Defaults fills zero-value fields with sensible values.
func (c *HandConfig) Defaults() {
	if c.Type == "" {
		c.Type = HandTypeSignalFollower
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
	if c.Position.MaxUnits == 0 {
		c.Position.MaxUnits = 1
	}
	if c.Risk.Exit == nil {
		c.Risk.Exit = &ExitConfig{SL: ExitLevelATR(2.0)}
	}
	if c.Strategy.Timeframe == "" {
		c.Strategy.Timeframe = "M1"
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
