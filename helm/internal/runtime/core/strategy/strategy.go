// Package strategy interprets trading signals and produces trade intents.
// It answers "what to trade?" — mapping raw signals from the signal engine
// into structured intents with direction, confidence, and urgency.
package strategy

import (
	"time"

	"github.com/shopspring/decimal"
)

// ── Direction ──────────────────────────────────────────────────────────────────

// Direction is the market direction emitted by a Rhai script.
// Maps directly to proto SignalMsg.dir; typed for compile-time safety.
type Direction string

const (
	DirLong  Direction = "long"
	DirShort Direction = "short"
	DirExit  Direction = "exit" // exit current position (direction-agnostic)
)

// ── Signal ─────────────────────────────────────────────────────────────────────

// Signal is the fully decoded signal from the herald engine.
// Populated from protobuf SignalMsg + NATS metadata by the dispatcher.
type Signal struct {
	Symbol    string
	Direction Direction // emitted by Rhai script
	Strength  float64   // conviction [0.0, 1.0]

	// Reference price at signal time (bar.close). Informational only.
	Price decimal.Decimal

	// Exit levels from the Rhai script.
	// When IsOffset=false: absolute price levels.
	// When IsOffset=true: deltas from fill price (e.g. StopPrice=-1.5 → SL 1.5 below fill).
	TargetPrice decimal.Decimal
	StopPrice   decimal.Decimal
	IsOffset    bool

	// ATR at signal time — forwarded from herald ledger.
	// Used by tactician for volatility-based position sizing.
	ATR decimal.Decimal

	// Reason is the human-readable explanation from the Rhai script (audit log only).
	Reason string

	// GeneratedAt is when the Rhai script emitted the signal (proto field t, unix ms).
	GeneratedAt time.Time

	// ReceivedAt is the NATS server ingestion timestamp.
	// Used for per-hand TTL checks; more accurate than time.Now() at dispatch.
	ReceivedAt time.Time
}

// IsUrgent reports whether this signal must not be dropped.
// Exit signals are urgent — dropping them leaves positions open.
func (s Signal) IsUrgent() bool {
	return s.Direction == DirExit
}

// ── Intent ─────────────────────────────────────────────────────────────────────

// Intent is the structured trade decision produced by a strategy.
type Intent struct {
	Signal     Signal  // originating signal — carries all execution hints
	Action     Action  // what to do
	Urgency    Urgency // how fast
	Confidence float64 // [0, 1] strategy's confidence in this trade
}

// ── Action ─────────────────────────────────────────────────────────────────────

// Action defines what kind of trade action to take.
type Action string

const (
	ActionEnterLong  Action = "enter_long"
	ActionEnterShort Action = "enter_short"
	ActionExitLong   Action = "exit_long"
	ActionExitShort  Action = "exit_short"
	ActionScaleIn    Action = "scale_in"
	ActionScaleOut   Action = "scale_out"
	ActionDoNothing  Action = "do_nothing"
)

// IsEntry reports whether this action opens a new position.
func (a Action) IsEntry() bool {
	return a == ActionEnterLong || a == ActionEnterShort || a == ActionScaleIn
}

// IsExit reports whether this action closes or reduces a position.
func (a Action) IsExit() bool {
	return a == ActionExitLong || a == ActionExitShort || a == ActionScaleOut
}

// ── Urgency ────────────────────────────────────────────────────────────────────

// Urgency defines how quickly the intent should be executed.
type Urgency string

const (
	UrgencyImmediate Urgency = "immediate" // market order, now
	UrgencyNormal    Urgency = "normal"    // limit order, reasonable timeframe
	UrgencyPatient   Urgency = "patient"   // TWAP / VWAP, spread over time
)

// ── Strategy interface ─────────────────────────────────────────────────────────

// Strategy interprets signals and produces trade intents.
type Strategy interface {
	Name() string
	Evaluate(signal Signal) Intent
}

// ── SignalFollower ─────────────────────────────────────────────────────────────

// SignalFollower is the canonical strategy for Rhai-driven hands.
// It maps herald's direction directly to an action, applying only a
// minimum-strength filter on entries. All execution hints (StopPrice,
// TargetPrice, ATR, IsOffset) pass through unchanged in intent.Signal.
type SignalFollower struct {
	minStrength float64
}

func NewSignalFollower(minStrength float64) *SignalFollower {
	if minStrength <= 0 {
		minStrength = 0.3
	}
	return &SignalFollower{minStrength: minStrength}
}

func (s *SignalFollower) Name() string { return "signal_follower" }

func (s *SignalFollower) Evaluate(sig Signal) Intent {
	intent := Intent{
		Signal:     sig,
		Confidence: sig.Strength,
	}

	// Exits always pass through — never drop a close signal.
	if sig.IsUrgent() {
		intent.Action = s.exitAction(sig)
		intent.Urgency = UrgencyImmediate
		if intent.Confidence == 0 {
			intent.Confidence = 1.0
		}
		return intent
	}

	// Weak entry signals are filtered.
	if sig.Strength < s.minStrength {
		intent.Action = ActionDoNothing
		return intent
	}

	switch sig.Direction {
	case DirLong:
		intent.Action = ActionEnterLong
	case DirShort:
		intent.Action = ActionEnterShort
	default:
		intent.Action = ActionDoNothing
	}

	switch {
	case sig.Strength >= 0.8:
		intent.Urgency = UrgencyImmediate
	case sig.Strength >= 0.5:
		intent.Urgency = UrgencyNormal
	default:
		intent.Urgency = UrgencyPatient
	}

	return intent
}

// exitAction maps an exit direction to the concrete exit action.
// DirExit is resolved against position side at the call site (hand_run.go).
func (s *SignalFollower) exitAction(_ Signal) Action {
	return ActionExitLong
}
