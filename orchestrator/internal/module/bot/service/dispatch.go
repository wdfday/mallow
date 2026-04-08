package service

import (
	"log/slog"
	"slices"
	"time"

	"orchestrator/internal/module/bot/domain"
	"orchestrator/internal/runtime"
)

// RunningBots returns all running bot instances.
func (s *Service) RunningBots() []*runtime.BotInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*runtime.BotInstance
	for _, bi := range s.bots {
		if bi.Bot.IsRunning() {
			out = append(out, bi)
		}
	}
	return out
}

// SignalFollowerBots returns running signal_follower bots watching the given symbol.
func (s *Service) SignalFollowerBots(symbol string) []*runtime.BotInstance {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*runtime.BotInstance
	for _, bi := range s.bots {
		if bi.Data.Config.Type != domain.BotTypeSignalFollower {
			continue
		}
		if !bi.Bot.IsRunning() {
			continue
		}
		if watchesSymbol(bi.Data.Config.Symbols, symbol) {
			out = append(out, bi)
		}
	}
	return out
}

// DispatchSignal pushes a signal to all running signal_follower bots watching the symbol.
//
// Urgent signals (close/exit): pushed to UrgentSignals — never silently dropped.
// Regular signals: drain-replace on Signals (buffer=1) so the bot always sees the
// latest entry signal, never a stale one that built up while an order was in flight.
func (s *Service) DispatchSignal(symbol, direction string, strength float64) {
	sig := runtime.Signal{
		Symbol:     symbol,
		Direction:  direction,
		Strength:   strength,
		ReceivedAt: time.Now(),
	}
	for _, bi := range s.SignalFollowerBots(symbol) {
		if sig.IsUrgent() {
			select {
			case bi.Bot.UrgentSignals <- sig:
			default:
				slog.Error("bot urgent signal channel full, dropping close signal",
					"bot_id", bi.Data.ID, "symbol", symbol)
			}
		} else {
			// Drain stale, push fresh — both non-blocking.
			select {
			case <-bi.Bot.Signals:
			default:
			}
			select {
			case bi.Bot.Signals <- sig:
			default:
				bi.Bot.RecordDrop()
				slog.Warn("bot signal channel full after drain, dropping",
					"bot_id", bi.Data.ID, "symbol", symbol)
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
