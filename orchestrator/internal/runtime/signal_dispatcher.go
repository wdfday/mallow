package runtime

import (
	"log/slog"
	"strings"
	"time"

	"orchestrator/internal/infra/engine"
)

// SignalDispatcher bridges NATS signal subjects into orchestrator-owned bot channels.
// Registry remains the owner of orchestrator runtimes; the dispatcher only resolves
// transport-level subjects into targeted runtime deliveries.
type SignalDispatcher struct {
	reg *Registry
}

func NewSignalDispatcher(reg *Registry) *SignalDispatcher {
	return &SignalDispatcher{reg: reg}
}

// Dispatch routes all signals in the response according to the NATS subject.
// Preferred mode is signals.{bot_id}; legacy symbol subjects are logged and ignored.
func (d *SignalDispatcher) Dispatch(subject string, resp *engine.SignalResponse) {
	target := strings.TrimPrefix(subject, engine.SubjBarsPrefix)
	target = strings.TrimPrefix(subject, "signals.")
	if target == "" {
		slog.Warn("signal dispatcher: empty subject target", "subject", subject)
		return
	}

	for _, sig := range resp.Signals {
		if sig == nil {
			continue
		}
		rsig := Signal{
			Symbol:     sig.S,
			Direction:  sig.Dir,
			Strength:   sig.Strength,
			ReceivedAt: time.Now(),
		}
		if d.reg.DispatchBotSignal(target, rsig) {
			slog.Info("signal dispatched",
				"bot_id", target,
				"symbol", sig.S,
				"direction", sig.Dir,
				"strength", sig.Strength,
			)
			continue
		}

		slog.Warn("signal dispatcher: no target bot found",
			"subject", subject,
			"target", target,
			"symbol", sig.S,
			"direction", sig.Dir,
		)
	}
}
