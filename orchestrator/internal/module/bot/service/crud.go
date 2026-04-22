package service

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"orchestrator/internal/module/bot/domain"
	"orchestrator/internal/runtime"
)

func (s *Service) Get(id string) (*runtime.BotInstance, error) { return s.getOrLoad(id) }

func (s *Service) GetBot(id string) *runtime.Bot {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return nil
	}
	return bi.Bot
}

func (s *Service) List() []domain.BotSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.BotSummary, 0, len(s.bots))
	for _, bi := range s.bots {
		out = append(out, bi.Summary())
	}
	return out
}

func (s *Service) ListByOrchestrator(orchID uuid.UUID) []domain.BotSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.BotSummary
	for _, bi := range s.bots {
		if bi.Data.OrchestratorID == orchID {
			out = append(out, bi.Summary())
		}
	}
	return out
}

func (s *Service) Create(cfg domain.BotConfig) (*runtime.BotInstance, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("bot name is required")
	}
	if cfg.OrchestratorID == uuid.Nil {
		return nil, fmt.Errorf("orchestrator_id is required")
	}
	if len(cfg.Symbols) == 0 {
		return nil, fmt.Errorf("at least one symbol is required")
	}
	cfg.Defaults()

	rt, err := s.registry.Get(cfg.OrchestratorID)
	if err != nil {
		return nil, fmt.Errorf("orchestrator runtime not found: %w", err)
	}
	if cfg.Market == domain.MarketTypeFutures {
		ft, ok := rt.Exchange.(interface{ SupportsFutures() bool })
		if !ok || !ft.SupportsFutures() {
			return nil, fmt.Errorf("exchange %q does not support futures trading", rt.Exchange.Name())
		}
	}

	id := s.repo.GenerateID()
	data := &domain.BotInstance{
		ID:        id,
		Status:    "stopped",
		CreatedAt: time.Now().UTC(),
	}
	data.ApplyConfig(cfg)

	if err := s.repo.Save(data); err != nil {
		return nil, fmt.Errorf("save bot: %w", err)
	}

	strat, tact := runtime.BuildBotComponents(data)
	bot := runtime.NewBot(id, cfg.OrchestratorID.String(), rt, strat, tact)
	setMeta(bot, data)
	rt.AddBot(bot)
	bi := &runtime.BotInstance{Data: data, Bot: bot, Exchange: rt.Exchange}
	s.mu.Lock()
	s.bots[id] = bi
	s.mu.Unlock()

	slog.Info("bot created", "id", id, "name", data.Name, "orchestrator_id", data.OrchestratorID)
	return bi, nil
}

// Update patches mutable fields: Name, Position sizing, and Risk exit rules.
// Symbols, Strategy, Type, and Market are immutable after creation.
// The bot must be stopped before updating.
func (s *Service) Update(id string, patch domain.BotConfig) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if bi.Bot.IsRunning() {
		return fmt.Errorf("bot %q is running — stop it first", id)
	}

	orchID := bi.Data.OrchestratorID

	// Phase 1: Persist to DB — pure domain mutation, no runtime side-effects.
	if err := s.repo.Update(id, func(d *domain.BotInstance) error {
		if patch.Name != "" {
			d.Name = patch.Name
		}
		if patch.Position.SizeMode != "" {
			d.Position = patch.Position
		}
		if patch.Risk.Exit != nil || patch.Risk.TrailingStopPct > 0 {
			d.Risk = patch.Risk
		}
		return nil
	}); err != nil {
		return err
	}

	// Phase 2: Reload and rebuild in-memory bot with updated config.
	updated, err := s.repo.Get(id)
	if err != nil {
		slog.Warn("bot updated in DB but reload failed — in-memory state stale", "id", id, "err", err)
		return nil
	}
	bi.Data = updated

	if rt, _ := s.registry.Get(orchID); rt != nil {
		rt.RemoveBot(id)
		strat, tact := runtime.BuildBotComponents(updated)
		bi.Bot = runtime.NewBot(updated.ID, orchID.String(), rt, strat, tact)
		setMeta(bi.Bot, updated)
		rt.AddBot(bi.Bot)
	}

	slog.Info("bot updated", "id", id, "name", updated.Name)
	return nil
}

func (s *Service) Delete(id string) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if bi.Bot.IsRunning() {
		return fmt.Errorf("bot %q is running — stop it first", id)
	}
	if rt, _ := s.registry.Get(bi.Data.OrchestratorID); rt != nil {
		rt.RemoveBot(id)
	}
	if err := s.repo.Delete(id); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.bots, id)
	s.mu.Unlock()
	slog.Info("bot deleted", "id", id)
	return nil
}

func setMeta(bot *runtime.Bot, b *domain.BotInstance) {
	if len(b.Symbols) > 0 {
		bot.Symbol = b.Symbols[0]
	}
	bot.StrategyName = b.Strategy.Key()
	bot.CapitalPct = b.Position.MaxPositionPct
}
