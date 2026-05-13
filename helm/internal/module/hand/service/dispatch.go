package service

import (
	"log/slog"
	"slices"
	"time"

	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/core/strategy"
)

// RunningHands returns all running hand instances.
func (s *Service) RunningHands() []*runtime.HandRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*runtime.HandRef
	for _, bi := range s.hands {
		if bi.Runner.IsRunning() {
			out = append(out, bi)
		}
	}
	return out
}

// SignalFollowerBots returns running signal_follower hands watching the given symbol.
func (s *Service) SignalFollowerBots(symbol string) []*runtime.HandRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*runtime.HandRef
	for _, bi := range s.hands {
		if bi.Data.Type != domain.HandTypeSignalFollower {
			continue
		}
		if !bi.Runner.IsRunning() {
			continue
		}
		if watchesSymbol([]string(bi.Data.Symbols), symbol) {
			out = append(out, bi)
		}
	}
	return out
}

// DispatchSignal pushes a signal to all running signal_follower hands watching the symbol.
//
// Urgent signals (close/exit): pushed to UrgentSignals — never silently dropped.
// Regular signals: drain-replace on Signals (buffer=1) so the hand always sees the
// latest entry signal, never a stale one that built up while an order was in flight.
func (s *Service) DispatchSignal(symbol, direction string, strength float64) {
	sig := runtime.Signal{
		Symbol:     symbol,
		Direction:  strategy.Direction(direction),
		Strength:   strength,
		ReceivedAt: time.Now(),
	}
	for _, bi := range s.SignalFollowerBots(symbol) {
		if sig.IsUrgent() {
			select {
			case bi.Runner.UrgentSignals <- sig:
			default:
				slog.Error("hand urgent signal channel full, dropping exit signal",
					"hand_id", bi.Data.ID, "symbol", symbol)
			}
		} else {
			// Drain stale, push fresh — both non-blocking.
			select {
			case <-bi.Runner.Signals:
			default:
			}
			select {
			case bi.Runner.Signals <- sig:
			default:
				bi.Runner.RecordDrop()
				slog.Warn("hand signal channel full after drain, dropping",
					"hand_id", bi.Data.ID, "symbol", symbol)
			}
		}
	}
}

func watchesSymbol(symbols []string, symbol string) bool {
	if len(symbols) == 0 {
		return true
	}
	return slices.Contains(symbols, symbol)
}
