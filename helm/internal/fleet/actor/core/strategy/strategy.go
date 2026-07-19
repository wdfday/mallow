// Package strategy interprets trading signals and produces trade intents.
// It answers "what to trade?" — mapping raw signals from the signal engine
// into structured intents with direction, confidence, and urgency.
//
// The package defines two layers:
//   - Types: Direction, Signal, Intent, Action, Urgency — the shared vocabulary
//     used by all layers in the runtime.
//   - Strategy interface + implementations: how a specific strategy interprets
//     a signal into an intent. Today the only implementation is SignalFollower
//     (signal_follower.go), which maps herald's direction field directly to an
//     action. Custom logic lives in the Rhai scripts at the herald layer, not here.
package strategy

import (
	"time"

	"github.com/shopspring/decimal"
)

// ── Direction ─────────────────────────────────────────────────────────────────

// Direction is the market direction emitted by a strategy script.
// Maps directly to proto SignalMsg.dir; typed for compile-time safety.
type Direction string

const (
	DirLong  Direction = "long"
	DirShort Direction = "short"
	DirExit  Direction = "exit" // exit current position, direction-agnostic
)

// ExitKind tags WHY a hand-internal DirExit signal was generated — set only by
// signal producers that already know the reason at generation time (the local
// exit monitor, position-desync detection); empty for herald-sourced signals
// and entries. Doesn't change how the signal is evaluated/placed — see Signal.ExitKind.
type ExitKind string

const (
	ExitKindTakeProfit ExitKind = "tp"
	ExitKindStopLoss   ExitKind = "sl"
	ExitKindOrphan     ExitKind = "orphaned" // matches the existing poslog/perf wire value
	// ExitKindSignal is a plain herald/NATS-originated DirExit — set explicitly by
	// the SignalDispatcher at construction time, not left as the empty-string
	// default that downstream code used to have to infer "signal" from.
	ExitKindSignal ExitKind = "signal"
)

// ── Signal ────────────────────────────────────────────────────────────────────

// Signal is the fully decoded signal from the herald engine.
// Populated from protobuf SignalMsg + NATS metadata by the SignalDispatcher.
type Signal struct {
	Symbol    string
	Direction Direction
	Strength  float64 // conviction [0.0, 1.0]; used as confidence and for urgency thresholds

	// ExitKind tags why this signal was generated, when the producer already
	// knows (see the ExitKind type doc). Empty for herald-sourced signals.
	ExitKind ExitKind

	// Reference price at signal time (bar.close). Informational only.
	Price decimal.Decimal

	// Exit levels from the script.
	// When IsOffset=false: absolute price levels.
	// When IsOffset=true: deltas from fill price (e.g. StopPrice=-1.5 means SL 1.5 below fill).
	TargetPrice decimal.Decimal
	StopPrice   decimal.Decimal
	IsOffset    bool

	// ATR at signal time — forwarded from the herald ledger.
	// Used by the Tactician for risk-based (ATR-fallback) position sizing.
	ATR decimal.Decimal

	// Reason is a human-readable explanation from the script (audit log only).
	Reason string

	// GeneratedAt is when the script emitted the signal (proto field t, unix ms).
	GeneratedAt time.Time

	// ReceivedAt is the NATS server ingestion timestamp.
	// Used for per-hand TTL checks; more accurate than time.Now() at dispatch time.
	ReceivedAt time.Time
}

// IsUrgent reports whether this signal must not be dropped.
// Exit signals are urgent — dropping them leaves positions unmanaged.
func (s Signal) IsUrgent() bool {
	return s.Direction == DirExit
}

// ── Intent ────────────────────────────────────────────────────────────────────

// Intent is the structured trade decision produced by a Strategy.
type Intent struct {
	Signal  Signal  // originating signal — carries all execution hints
	Action  Action  // what to do
	Urgency Urgency // how fast
	// Reason is populated when Action == ActionDoNothing — explains why the
	// strategy chose not to act (e.g. "strength below min", "direction mismatch",
	// or a Rhai-script-level explanation). Empty for all other actions.
	Reason string
}

// ── Action ────────────────────────────────────────────────────────────────────

// Action defines what kind of trade action to take.
type Action string

const (
	ActionEnterLong  Action = "enter_long"
	ActionEnterShort Action = "enter_short"
	ActionExitLong   Action = "exit_long"
	ActionExitShort  Action = "exit_short"
	// ActionScaleIn / ActionScaleOut are design stubs for partial entry and exit.
	// - ScaleIn: add to an existing position (pyramid add). See design-scale-out.md.
	// - ScaleOut: partially close a position. See design-scale-out.md.
	// Tactician routes them to buy/sell sides; hand_runner does not yet act on them.
	ActionScaleIn   Action = "scale_in"
	ActionScaleOut  Action = "scale_out"
	ActionDoNothing Action = "do_nothing"
)

// IsEntry reports whether this action opens or adds to a position.
func (a Action) IsEntry() bool {
	return a == ActionEnterLong || a == ActionEnterShort || a == ActionScaleIn
}

// IsExit reports whether this action closes or reduces a position.
func (a Action) IsExit() bool {
	return a == ActionExitLong || a == ActionExitShort || a == ActionScaleOut
}

// ── Urgency ───────────────────────────────────────────────────────────────────

// Urgency defines how quickly an intent should be executed.
type Urgency string

const (
	UrgencyImmediate Urgency = "immediate" // market order, execute now
	UrgencyNormal    Urgency = "normal"    // limit order, reasonable timeframe
	UrgencyPatient   Urgency = "patient"   // TWAP slices; declared in Tactician.Plan but TwapExecutor not yet implemented — see design-twap.md
)

// ── Strategy interface ────────────────────────────────────────────────────────

// Strategy interprets a raw signal and produces a trade intent.
// Implementations live in this package (SignalFollower) or can be injected
// for testing. Custom trading logic lives in herald Rhai scripts, not here.
type Strategy interface {
	Name() string
	Evaluate(signal Signal) Intent
}
