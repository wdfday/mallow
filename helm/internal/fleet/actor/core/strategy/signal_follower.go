package strategy

import "fmt"

// SignalFollower is the canonical Strategy for Rhai-script-driven hands.
// It maps herald's direction signal directly to a trade action, applying only
// a minimum-strength filter on entries. All execution hints (StopPrice,
// TargetPrice, ATR, IsOffset) pass through unchanged in intent.Signal.
//
// Urgency is always Immediate for passing signals — execution type (market vs
// limit) is determined by hand config (OrderTypeMarket / OrderTypeLimit), not
// by strength tiers. Tiered urgency based on magic strength thresholds (0.8/0.5)
// had no effect on market hands (99% of use cases) and mapped signals below 0.5
// to UrgencyPatient (TWAP), which is not implemented.
type SignalFollower struct {
	minStrength float64
}

// NewSignalFollower creates a SignalFollower. minStrength must be in (0, 1];
// values ≤ 0 are clamped to 0.3.
func NewSignalFollower(minStrength float64) *SignalFollower {
	if minStrength <= 0 {
		minStrength = 0.3
	}
	return &SignalFollower{minStrength: minStrength}
}

func (s *SignalFollower) Name() string { return "signal_follower" }

// Evaluate translates a raw Signal into a structured Intent.
// Exit signals are always passed through with Immediate urgency — they are never dropped.
// Entry signals that pass minStrength are also Immediate — execution type is a hand-level
// config (market vs limit), not a per-signal strength decision.
func (s *SignalFollower) Evaluate(sig Signal) Intent {
	intent := Intent{Signal: sig}

	// Exits always pass — never drop a close signal.
	if sig.IsUrgent() {
		intent.Action = s.exitAction(sig)
		intent.Urgency = UrgencyImmediate
		return intent
	}

	// Weak entry signals are filtered.
	if sig.Strength < s.minStrength {
		intent.Action = ActionDoNothing
		intent.Reason = fmt.Sprintf("strength %.2f below min %.2f", sig.Strength, s.minStrength)
		return intent
	}

	switch sig.Direction {
	case DirLong:
		intent.Action = ActionEnterLong
	case DirShort:
		intent.Action = ActionEnterShort
	default:
		intent.Action = ActionDoNothing
		intent.Reason = fmt.Sprintf("unrecognised direction %q", sig.Direction)
	}

	// All passing entry signals are Immediate. The hand's OrderType config
	// (market/limit) determines execution — not strength tiers.
	intent.Urgency = UrgencyImmediate
	return intent
}

// exitAction maps an exit signal to a concrete exit action.
// DirExit is resolved against the live position side in hand_runner.go.
func (s *SignalFollower) exitAction(_ Signal) Action {
	return ActionExitLong
}
