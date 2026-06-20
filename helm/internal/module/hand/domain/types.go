package domain

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// HandStatus is the persisted lifecycle state of a hand.
type HandStatus string

const (
	HandStatusStopped  HandStatus = "stopped"
	HandStatusRunning  HandStatus = "running"
	HandStatusKilled   HandStatus = "killed"   // legacy terminal: only exists in DB from before kill API was removed
	HandStatusReleased HandStatus = "released" // terminal: positions orphaned
)

// IsTerminal reports whether the status is a permanent end-state.
// Terminal hands cannot be started or killed again — they exist for record-keeping only.
func (s HandStatus) IsTerminal() bool {
	return s == HandStatusKilled || s == HandStatusReleased
}

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
// Script is the full script source sent as params["script"] to herald's
// script_strategy; indicators declared via ind.TYPE(period) syntax.
type StrategySpec struct {
	// Script — full source code (required)
	Script string `json:"script" binding:"required"`

	// Timeframe the script operates on (e.g. "M1", "M5", "H1", "D1") — REQUIRED.
	// Strategies are calibrated to a specific TF, so we never silently default;
	// missing TF is a validation error. herald rejects empty TF too.
	Timeframe string `json:"timeframe" binding:"required,oneof=M1 M5 M15 M30 H1 H4 D1 W1"`

	// Signal strength filter [0–1]
	MinStrength float64 `json:"min_strength,omitempty" binding:"omitempty,gte=0,lte=1"`
}

// validTimeframes is the set of timeframe strings accepted by herald.
var validTimeframes = map[string]bool{
	"M1": true, "M5": true, "M15": true, "M30": true,
	"H1": true, "H4": true, "D1": true, "W1": true,
}

// Validate checks that Script is non-empty and optional fields are valid.
func (s StrategySpec) Validate() error {
	if s.Script == "" {
		return fmt.Errorf("strategy: script is required")
	}
	if s.Timeframe != "" && !validTimeframes[s.Timeframe] {
		return fmt.Errorf("strategy: invalid timeframe %q (valid: M1 M5 M15 M30 H1 H4 D1 W1)", s.Timeframe)
	}
	return nil
}

func (s StrategySpec) Value() (driver.Value, error) { return jsonValue(s) }
func (s *StrategySpec) Scan(src any) error          { return jsonScan(src, s) }

// ── Enums ─────────────────────────────────────────────────────────────────────

// SizeMode defines the position-sizing algorithm.
type SizeMode string

const (
	SizeModeFixedFractional SizeMode = "fixed_fractional" // Ralph Vince: risk f×equity / stop (SL → ATR fallback)
	SizeModeFixedQty        SizeMode = "fixed_qty"        // fixed base quantity (× signal strength)
	SizeModeQuoteQty        SizeMode = "quote_qty"        // fixed quote spend per trade (× signal strength)
	SizeModePercentEquity   SizeMode = "percent_equity"   // notional: UnitPct × equity (× strength)
	SizeModeVolatility      SizeMode = "volatility"       // fixed_fractional with the stop forced to ATR
)

// OrderType is the default entry order type for a hand.
type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

// LimitFallback defines what happens when a limit order times out unfilled.
type LimitFallback string

const (
	LimitFallbackCancel LimitFallback = "cancel" // cancel and do nothing
	LimitFallbackMarket LimitFallback = "market" // re-place as market order
)

// MarginType defines the futures margin mode.
type MarginType string

const (
	MarginTypeIsolated MarginType = "isolated"
	MarginTypeCross    MarginType = "cross"
)

// ── PositionConfig ────────────────────────────────────────────────────────────

// PositionConfig controls per-trade sizing and scaling behaviour.
// Capital allocation (AllocatedCapital) and operational timing (SignalTTLSec) live
// as first-class columns on Hand, not inside this JSONB blob.
//
// Exactly one sizing param is active per SizeMode:
//
//	fixed_fractional → RiskPerTradePct  (Ralph Vince: f×equity / stop; SL → ATR fallback)
//	volatility       → RiskPerTradePct  (same, stop forced to ATR)
//	percent_equity   → UnitPct         (fraction of allocated equity per unit)
//	fixed_qty        → FixedQty
//	quote_qty        → FixedQuoteQty   (fixed USDT notional; use this for absolute sizing)
type PositionConfig struct {
	// SizeMode selects the sizing algorithm. Defaults to fixed_fractional.
	SizeMode SizeMode `json:"size_mode,omitempty"`

	// ── Sizing params — only the one matching SizeMode is read ──────────────

	// RiskPerTradePct: the fixed fraction f of equity risked per trade (Ralph Vince).
	// Used by: fixed_fractional, volatility.  e.g. 0.01 = risk 1% of equity per trade.
	RiskPerTradePct float64 `json:"risk_per_trade_pct,omitempty"`

	// UnitPct: fraction of allocated equity deployed per entry unit.
	// Used by: percent_equity.  e.g. 0.10 = 10% of allocated equity per unit.
	UnitPct float64 `json:"unit_pct,omitempty"`

	// FixedQty: fixed base-asset quantity per trade.
	// Used by: fixed_qty.
	FixedQty decimal.Decimal `json:"fixed_qty,omitempty"`

	// FixedQuoteQty: fixed quote spend per trade (e.g. 1000 USDT).
	// Used by: quote_qty. Only for market-buy on spot; exchange fills max base qty.
	FixedQuoteQty decimal.Decimal `json:"fixed_quote_qty,omitempty"`

	// ── Scaling ─────────────────────────────────────────────────────────────

	// MaxUnits: max concurrent entry legs. 1 = no scaling (default).
	// Each signal while at max is rejected.
	// Overridden downward by helm-level RiskConfig.MaxUnitsPerHand if set.
	MaxUnits int `json:"max_units,omitempty"`

	// Pyramid: how additional entry signals are handled while a position is open.
	// true  → merge into the existing leg (qty accumulates, avg_entry recalculated).
	// false → open a new independent leg up to MaxUnits.
	Pyramid bool `json:"pyramid,omitempty"`

	// ── Caps ────────────────────────────────────────────────────────────────

	// MaxPositionPct: hard cap on total open exposure as a fraction of equity.
	// Legacy fallback — prefer AllocatedCapital for isolation.
	MaxPositionPct float64 `json:"max_position_pct,omitempty"`

	// StrengthSizing: when true (default), notional modes (percent_equity, fixed_qty,
	// quote_qty) multiply size by signal strength [0–1]. Risk-based modes
	// (fixed_fractional, volatility) always ignore this flag.
	// nil → true (backward-compatible with rows written before this field existed).
	StrengthSizing *bool `json:"strength_sizing,omitempty"`
}

func (p PositionConfig) Value() (driver.Value, error) { return jsonValue(p) }
func (p *PositionConfig) Scan(src any) error          { return jsonScan(src, p) }

// ── HandGuardConfig ─────────────────────────────────────────────────────────────

// HandGuardConfig is the per-hand circuit breaker (edge-degradation guard), NOT
// per-trade risk — sizing/stop risk lives in PositionConfig, portfolio-level risk in
// HelmConfig. It tracks a sliding window of the last WindowTrades closed trades and
// auto-pauses the hand when any enabled threshold is breached.
// All threshold fields are optional — zero means disabled. Percentages are taken against
// the hand's AllocatedCapital (required: alloc=0 hands cannot arm these guards).
type HandGuardConfig struct {
	// WindowTrades is the number of most-recent closed trades to evaluate.
	// 0 disables all edge-degradation checks below.
	WindowTrades int `json:"window_trades,omitempty"`

	// MaxTotalLossPct auto-stops the hand when sum(PnL over window) / AllocatedCapital
	// drops below -X. e.g. 0.05 = stop when the window's cumulative loss exceeds 5%.
	MaxTotalLossPct float64 `json:"max_total_loss_pct,omitempty"`

	// MaxAvgLossPct auto-stops the hand when avg(PnL over window) / AllocatedCapital
	// drops below -X. Catches consistent small losses even if the total is modest.
	MaxAvgLossPct float64 `json:"max_avg_loss_pct,omitempty"`

	// MaxSingleLossPct auto-stops the hand when any single trade in the window
	// lost more than X of AllocatedCapital. Guards against blow-up trades.
	MaxSingleLossPct float64 `json:"max_single_loss_pct,omitempty"`

	// MaxConsecLoss auto-stops the hand after N consecutive losing trades.
	// Resets to 0 on any winning trade.
	MaxConsecLoss int `json:"max_consec_loss,omitempty"`
}

func (r HandGuardConfig) Value() (driver.Value, error) { return jsonValue(r) }
func (r *HandGuardConfig) Scan(src any) error          { return jsonScan(src, r) }

// ── FuturesConfig ─────────────────────────────────────────────────────────────

// FuturesConfig holds futures-specific parameters.
// Only meaningful when Hand.Market == MarketTypeFutures.
type FuturesConfig struct {
	Leverage   int        `json:"leverage"`    // e.g. 10 for 10x; 1 = no leverage
	MarginType MarginType `json:"margin_type"` // "isolated" | "cross"
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
	Guard    HandGuardConfig
	Futures  *FuturesConfig

	// Top-level fields — mirror the dedicated columns on Hand.
	AllocatedCapital decimal.Decimal
	SignalTTLSec     int
	OrderType        OrderType
	LimitTimeoutSec  int
	LimitFallback    LimitFallback
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
		c.Futures = &FuturesConfig{Leverage: 1, MarginType: MarginTypeIsolated}
	}
	if c.Position.SizeMode == "" {
		c.Position.SizeMode = SizeModeFixedFractional
	}
	if c.Position.RiskPerTradePct == 0 {
		c.Position.RiskPerTradePct = 0.01
	}
	if c.Position.MaxPositionPct == 0 {
		c.Position.MaxPositionPct = 0.20
	}
	if c.Position.UnitPct == 0 {
		c.Position.UnitPct = 0.10
	}
	if c.Position.MaxUnits == 0 {
		c.Position.MaxUnits = 1
	}
	if c.Strategy.Timeframe == "" {
		c.Strategy.Timeframe = "M1"
	}
	if c.Strategy.MinStrength == 0 {
		c.Strategy.MinStrength = 0.3
	}
	if c.OrderType == "" {
		c.OrderType = OrderTypeMarket
	}
	if c.OrderType == OrderTypeLimit && c.LimitTimeoutSec > 0 && c.LimitFallback == "" {
		c.LimitFallback = LimitFallbackCancel
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
