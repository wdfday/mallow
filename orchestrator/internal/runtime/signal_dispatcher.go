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

func signalTargetFromSubject(subject string) string {
	target := strings.TrimPrefix(subject, engine.SubjBarsPrefix)
	return strings.TrimPrefix(target, "signals.")
}

// Dispatch routes all signals in the response according to the NATS subject.
// Preferred mode is signals.{bot_id}; legacy symbol subjects are logged and ignored.
func (d *SignalDispatcher) Dispatch(subject string, resp *engine.SignalResponse) {
	target := signalTargetFromSubject(subject)
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
		if d.reg.EnqueueSignal(RoutedSignal{
			BotID:   target,
			Subject: subject,
			Signal:  rsig,
		}) {
			continue
		}

		slog.Warn("signal dispatcher: registry ingress full",
			"subject", subject,
			"target", target,
			"symbol", sig.S,
			"direction", sig.Dir,
		)
	}
}
