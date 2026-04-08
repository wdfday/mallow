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
		ID:             id,
		OrchestratorID: cfg.OrchestratorID,
		Config:         cfg,
		Status:         "stopped",
		CreatedAt:      time.Now().UTC(),
	}
	if err := s.repo.Save(data); err != nil {
		return nil, fmt.Errorf("save bot: %w", err)
	}

	strat, tact := runtime.BuildBotComponents(cfg)
	bot := runtime.NewBot(id, cfg.OrchestratorID.String(), rt, strat, tact)
	setMeta(bot, cfg)
	rt.AddBot(bot)
	bi := &runtime.BotInstance{Data: data, Bot: bot, Exchange: rt.Exchange}
	s.mu.Lock()
	s.bots[id] = bi
	s.mu.Unlock()

	slog.Info("bot created", "id", id, "name", cfg.Name, "orchestrator_id", cfg.OrchestratorID)
	return bi, nil
}

func (s *Service) Update(id string, patch domain.BotConfig) error {
	bi, err := s.getOrLoad(id)
	if err != nil {
		return err
	}
	if bi.Bot.IsRunning() {
		return fmt.Errorf("bot %q is running — stop it first", id)
	}

	return s.repo.Update(id, func(d *domain.BotInstance) error {
		if patch.Name != "" {
			d.Config.Name = patch.Name
		}
		if patch.Type != "" {
			d.Config.Type = patch.Type
		}
		if patch.Symbols != nil {
			d.Config.Symbols = patch.Symbols
		}
		if patch.Tactic.StrategyName() != "" {
			d.Config.Tactic = patch.Tactic
		}
		if patch.Risk.SizeMode != "" {
			d.Config.Risk.SizeMode = patch.Risk.SizeMode
		}
		if patch.Risk.RiskPerTradePct != 0 {
			d.Config.Risk.RiskPerTradePct = patch.Risk.RiskPerTradePct
		}
		if patch.Risk.MaxPositionPct != 0 {
			d.Config.Risk.MaxPositionPct = patch.Risk.MaxPositionPct
		}
		if patch.Risk.FixedQty.IsPositive() {
			d.Config.Risk.FixedQty = patch.Risk.FixedQty
		}
		if patch.Risk.StopLossATRMult != 0 {
			d.Config.Risk.StopLossATRMult = patch.Risk.StopLossATRMult
		}

		if patch.OrchestratorID != uuid.Nil && patch.OrchestratorID != d.OrchestratorID {
			rt, err := s.registry.Get(patch.OrchestratorID)
			if err != nil {
				return fmt.Errorf("new orchestrator runtime not found: %w", err)
			}
			if oldRT, _ := s.registry.Get(d.OrchestratorID); oldRT != nil {
				oldRT.RemoveBot(id)
			}
			d.OrchestratorID = patch.OrchestratorID
			d.Config.OrchestratorID = patch.OrchestratorID
			strat, tact := runtime.BuildBotComponents(d.Config)
			bi.Exchange = rt.Exchange
			bi.Bot = runtime.NewBot(d.ID, patch.OrchestratorID.String(), rt, strat, tact)
			setMeta(bi.Bot, d.Config)
			rt.AddBot(bi.Bot)
		} else {
			if rt, _ := s.registry.Get(d.OrchestratorID); rt != nil {
				strat, tact := runtime.BuildBotComponents(d.Config)
				bi.Bot = runtime.NewBot(d.ID, d.OrchestratorID.String(), rt, strat, tact)
				setMeta(bi.Bot, d.Config)
				rt.AddBot(bi.Bot)
			}
		}
		slog.Info("bot updated", "id", id)
		return nil
	})
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

// setMeta copies display metadata from config onto the bot.
func setMeta(bot *runtime.Bot, cfg domain.BotConfig) {
	if len(cfg.Symbols) > 0 {
		bot.Symbol = cfg.Symbols[0]
	}
	bot.StrategyName = cfg.Tactic.StrategyName()
	bot.CapitalPct = cfg.Risk.MaxPositionPct
}
