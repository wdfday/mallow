package safe

import (
	"log/slog"
	"runtime/debug"
)

// Recover is intended to be called as `defer safe.Recover()` at the top of any
// goroutine body. It catches a panic, logs it with a full stack trace, and lets
// the rest of the program continue instead of crashing the process.
func Recover() {
	r := recover()
	if r == nil {
		return
	}
	slog.Error("goroutine panic recovered",
		"recover", r,
		"stack", string(debug.Stack()),
	)
}

// RecoverHand is Recover for a per-hand goroutine: it logs through the hand's
// own logger, which is pre-tagged with hand_id + helm_id, so the panic line
// identifies exactly which hand crashed. Pass h.log.
// Usage: `defer safe.RecoverHand(h.log)`.
func RecoverHand(log *slog.Logger) {
	r := recover()
	if r == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	log.Error("hand goroutine panic recovered",
		"recover", r,
		"stack", string(debug.Stack()),
	)
}
