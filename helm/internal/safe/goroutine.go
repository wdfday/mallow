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
