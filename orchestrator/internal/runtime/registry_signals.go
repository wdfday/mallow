package runtime

import (
	"context"
	"log/slog"
)

const defaultRegistrySignalBuffer = 256

// RoutedSignal is the registry-owned message envelope for signal ingress.
// Transport adapters decode external payloads into this value and enqueue it.
type RoutedSignal struct {
	BotID   string
	Subject string
	Signal  Signal
}

// EnqueueSignal pushes a decoded signal into the registry ingress buffer.
// Returns false when the buffer is full and the signal is dropped.
func (r *Registry) EnqueueSignal(msg RoutedSignal) bool {
	select {
	case r.signalCh <- msg:
		return true
	default:
		slog.Warn("registry: signal buffer full, dropping",
			"bot_id", msg.BotID,
			"subject", msg.Subject,
			"symbol", msg.Signal.Symbol,
			"direction", msg.Signal.Direction,
		)
		return false
	}
}

func (r *Registry) startSignalLoop(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	r.signalLoopOnce.Do(func() {
		go r.runSignalLoop(ctx)
	})
}

func (r *Registry) runSignalLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-r.signalCh:
			if r.DispatchBotSignal(msg.BotID, msg.Signal) {
				slog.Info("signal dispatched",
					"bot_id", msg.BotID,
					"subject", msg.Subject,
					"symbol", msg.Signal.Symbol,
					"direction", msg.Signal.Direction,
					"strength", msg.Signal.Strength,
				)
				continue
			}

			slog.Warn("registry: no target bot found",
				"subject", msg.Subject,
				"target", msg.BotID,
				"symbol", msg.Signal.Symbol,
				"direction", msg.Signal.Direction,
			)
		}
	}
}
