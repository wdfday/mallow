package strategy

import "fmt"

// SignalFollower is the canonical Strategy for Rhai-script-driven hands.
// It maps herald's direction signal directly to a trade action, applying only
// a minimum-strength filter on entries. All execution hints (StopPrice,
// TargetPrice, ATR, IsOffset) pass through unchanged in intent.Signal.
//
// Urgency is derived from signal strength:
//   - strength ≥ 0.8 → Immediate (market order)
//   - strength ≥ 0.5 → Normal    (limit order)
//   - strength  < 0.5 → Patient   (TWAP — declared, not yet implemented)
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

// exitAction maps an exit signal to a concrete exit action.
// DirExit is resolved against the live position side in hand_runner.go.
func (s *SignalFollower) exitAction(_ Signal) Action {
	return ActionExitLong
}
