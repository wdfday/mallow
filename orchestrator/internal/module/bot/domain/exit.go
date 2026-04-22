package domain

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ExitLevel mirrors almanac's ExitLevel: either a fixed fraction (number) or
// an ATR-multiplier expression (string).
//
// JSON: 0.05 → fixed 5%; "2*atr" → 2× ATR(14); "1.5*atr(21)" → 1.5× ATR(21).
type ExitLevel struct {
	Pct  *float64 // non-nil when fixed fraction
	Expr *string  // non-nil when ATR expression
}

// ExitLevelPct creates a fixed-fraction ExitLevel.
func ExitLevelPct(v float64) *ExitLevel { return &ExitLevel{Pct: &v} }

// ExitLevelATR creates an ATR-multiplier ExitLevel, e.g. ExitLevelATR(2.0) → "2*atr".
func ExitLevelATR(mult float64) *ExitLevel {
	s := fmt.Sprintf("%g*atr", mult)
	return &ExitLevel{Expr: &s}
}

func (e ExitLevel) MarshalJSON() ([]byte, error) {
	if e.Pct != nil {
		return json.Marshal(*e.Pct)
	}
	if e.Expr != nil {
		return json.Marshal(*e.Expr)
	}
	return []byte("null"), nil
}

func (e *ExitLevel) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		e.Pct = &f
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		e.Expr = &s
		return nil
	}
	return fmt.Errorf("exit level must be a number or ATR expression string")
}

// ATRMult parses an ATR-multiplier expression and returns (mult, true).
// Returns (0, false) for fixed-pct levels or unparseable expressions.
//
// Accepted forms: "2*atr", "1.5*atr", "2*atr(21)".
func (e *ExitLevel) ATRMult() float64 {
	if e == nil || e.Expr == nil {
		return 0
	}
	s := strings.ToLower(strings.TrimSpace(*e.Expr))
	// strip optional "(period)" suffix
	if idx := strings.Index(s, "*atr("); idx > 0 {
		s = s[:idx] + "*atr"
	}
	multStr, found := strings.CutSuffix(s, "*atr")
	if !found {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(multStr), 64)
	if err != nil {
		return 0
	}
	return v
}

// PctValue returns the fixed-fraction value, or 0 when this is an ATR expression.
func (e *ExitLevel) PctValue() float64 {
	if e == nil || e.Pct == nil {
		return 0
	}
	return *e.Pct
}

// ExitConfig mirrors almanac's ExitConfig for backtest/live-trading parity.
// All fields optional — absent means strategy signal-based exits are used.
type ExitConfig struct {
	TP      *ExitLevel `json:"tp,omitempty"`       // take-profit
	SL      *ExitLevel `json:"sl,omitempty"`       // stop-loss
	MaxBars *int       `json:"max_bars,omitempty"` // time-stop: close after N bars
}
