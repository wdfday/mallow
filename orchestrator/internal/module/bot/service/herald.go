package service

import (
	"context"
	"log/slog"
	"time"

	"orchestrator/internal/infra/engine"
	"orchestrator/internal/module/bot/domain"
)

// heraldRegister sends engine.register to herald for every symbol the bot watches.
// Errors are logged and non-fatal — the bot still starts locally.
func (s *Service) heraldRegister(botID string, cfg domain.BotConfig) {
	if s.herald == nil {
		return
	}
	paramsJSON, err := cfg.Tactic.ParamsJSON()
	if err != nil {
		slog.Warn("herald register: marshal params failed", "bot_id", botID, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, sym := range cfg.Symbols {
		req := &engine.RegisterMsg{
			BotId:      botID,
			Symbol:     sym,
			Strategy:   cfg.Tactic.StrategyName(),
			ParamsJson: paramsJSON,
			OrchId:     cfg.OrchestratorID.String(),
		}
		if ack, err := s.herald.Register(ctx, req); err != nil {
			slog.Warn("herald register failed", "bot_id", botID, "symbol", sym, "err", err)
		} else {
			slog.Info("herald registered", "bot_id", botID, "symbol", sym, "ack", ack)
		}
	}
}

// heraldDeregister sends engine.deregister to herald for the given bot.
func (s *Service) heraldDeregister(botID string) {
	if s.herald == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.herald.Deregister(ctx, botID); err != nil {
		slog.Warn("herald deregister failed", "bot_id", botID, "err", err)
	}
}
