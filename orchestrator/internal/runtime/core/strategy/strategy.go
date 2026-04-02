// Package strategy interprets trading signals and produces trade intents.
// It answers "what to trade?" — mapping raw signals from the signal engine
// into structured intents with direction, confidence, and urgency.
package strategy

import "time"

// Signal is a raw signal from the signal engine.
type Signal struct {
	Symbol    string    `json:"symbol"`
	Direction string    `json:"direction"` // "long", "short", "close"
	Strength  float64   `json:"strength"`  // [0, 1]
	Timestamp time.Time `json:"timestamp"`
}

// Intent is a structured trade intent produced by a strategy.
type Intent struct {
	Signal     Signal  `json:"signal"`
	Action     Action  `json:"action"`     // what to do
	Urgency    Urgency `json:"urgency"`    // how fast
	Confidence float64 `json:"confidence"` // [0, 1] strategy's confidence in this trade
}

// Action defines what kind of trade action to take.
type Action string

const (
	ActionEnterLong  Action = "enter_long"
	ActionEnterShort Action = "enter_short"
	ActionExitLong   Action = "exit_long"
	ActionExitShort  Action = "exit_short"
	ActionScaleIn    Action = "scale_in"  // add to existing position
	ActionScaleOut   Action = "scale_out" // reduce existing position
	ActionDoNothing  Action = "do_nothing"
)

// Urgency defines how quickly the intent should be executed.
type Urgency string

const (
	UrgencyImmediate Urgency = "immediate" // market order, now
	UrgencyNormal    Urgency = "normal"    // limit order, reasonable timeframe
	UrgencyPatient   Urgency = "patient"   // TWAP / VWAP, spread over time
)

// Strategy interprets signals and produces trade intents.
type Strategy interface {
	// Name returns the strategy identifier.
	Name() string

	// Evaluate processes a signal and returns a trade intent.
	// May return ActionDoNothing if the signal doesn't meet strategy criteria.
	Evaluate(signal Signal) Intent
}

// ── Built-in strategies ────────────────────────────────────────────────────

// SignalFollower is the simplest strategy: follow signal engine's direction directly.
// Maps signal direction to action, strength to confidence.
type SignalFollower struct {
	minStrength float64 // minimum signal strength to act on
}

// NewSignalFollower creates a SignalFollower with the given minimum strength threshold.
func NewSignalFollower(minStrength float64) *SignalFollower {
	if minStrength <= 0 {
		minStrength = 0.3 // default: ignore weak signals
	}
	return &SignalFollower{minStrength: minStrength}
}

func (s *SignalFollower) Name() string { return "signal_follower" }

func (s *SignalFollower) Evaluate(signal Signal) Intent {
	intent := Intent{
		Signal:     signal,
		Confidence: signal.Strength,
	}

	// Filter weak signals.
	if signal.Strength < s.minStrength {
		intent.Action = ActionDoNothing
		return intent
	}

	// Map direction to action.
	switch signal.Direction {
	case "long":
		intent.Action = ActionEnterLong
	case "short":
		intent.Action = ActionEnterShort
	case "close":
		intent.Action = ActionExitLong // default: close means exit long
	default:
		intent.Action = ActionDoNothing
	}

	// Map strength to urgency.
	switch {
	case signal.Strength >= 0.8:
		intent.Urgency = UrgencyImmediate
	case signal.Strength >= 0.5:
		intent.Urgency = UrgencyNormal
	default:
		intent.Urgency = UrgencyPatient
	}

	return intent
}
