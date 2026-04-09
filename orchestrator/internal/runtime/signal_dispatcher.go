package runtime

import (
	"log/slog"
	"time"

	"orchestrator/internal/infra/engine"
)

// SignalDispatcher bridges the NATS "signals" subject into Orchestrator bot channels.
// Both orch_id and bot_id are read from SignalResponse payload — subject is fixed "signals".
type SignalDispatcher struct {
	sink SignalSink
}

func NewSignalDispatcher(sink SignalSink) *SignalDispatcher {
	return &SignalDispatcher{sink: sink}
}

// Dispatch routes all signals in the response to the target orchestrator/bot.
// Called directly from the NATS subscription goroutine — must not block.
func (d *SignalDispatcher) Dispatch(resp *engine.SignalResponse) {
	orchID := resp.OrchId
	botID := resp.BotId
	if orchID == "" || botID == "" {
		slog.Warn("signal dispatcher: missing orch_id or bot_id in payload",
			"orch_id", orchID, "bot_id", botID)
		return
	}

	for _, sig := range resp.Signals {
		if sig == nil {
			continue
		}
		d.sink.RouteSignal(orchID, botID, Signal{
			Symbol:     sig.S,
			Direction:  sig.Dir,
			Strength:   sig.Strength,
			ReceivedAt: time.Now(),
		})
	}
}
