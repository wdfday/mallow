package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"orchestrator/internal/infra/engine"
	"orchestrator/internal/module/bot/domain"
	"orchestrator/internal/runtime"
)

// heraldClient is the subset of engine.SignalClient used by the service.
type heraldClient interface {
	Register(ctx context.Context, req *engine.RegisterMsg) (string, error)
	Deregister(ctx context.Context, botID string) error
	ListBots(ctx context.Context) (*engine.BotListResponse, error)
}

// Service is the CRUD + lifecycle layer for bots.
// Business logic (signal handling, order placement, portfolio) lives in runtime.Bot.
type Service struct {
	repo     domain.BotRepo
	registry *runtime.Registry
	herald   heraldClient // nil when NATS unavailable (dev/test)

	mu   sync.RWMutex
	bots map[string]*runtime.BotInstance
}

func NewService(r domain.BotRepo, registry *runtime.Registry, herald heraldClient) *Service {
	s := &Service{
		repo:     r,
		registry: registry,
		herald:   herald,
		bots:     make(map[string]*runtime.BotInstance),
	}
	for _, data := range r.All() {
		bi, err := s.hydrate(data)
		if err != nil {
			slog.Warn("bot hydrate skipped", "id", data.ID, "err", err)
			continue
		}
		s.bots[data.ID] = bi
	}
	return s
}

func (s *Service) hydrate(data *domain.BotInstance) (*runtime.BotInstance, error) {
	rt, err := s.registry.Get(data.OrchestratorID)
	if err != nil {
		return nil, fmt.Errorf("no runtime for orchestrator %q: %w", data.OrchestratorID, err)
	}
	strat, tact := runtime.BuildBotComponents(data)
	bot := runtime.NewBot(data.ID, data.OrchestratorID.String(), rt, strat, tact)
	setMeta(bot, data)
	rt.AddBot(bot)
	if data.Status == "running" {
		s.heraldRegister(data.ID, data)
		bot.Start()
	}
	return &runtime.BotInstance{Data: data, Bot: bot, Exchange: rt.Exchange}, nil
}

func (s *Service) getOrLoad(id string) (*runtime.BotInstance, error) {
	s.mu.RLock()
	bi, ok := s.bots[id]
	s.mu.RUnlock()
	if ok {
		return bi, nil
	}
	data, err := s.repo.Get(id)
	if err != nil {
		return nil, err
	}
	bi, err = s.hydrate(data)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.bots[id] = bi
	s.mu.Unlock()
	return bi, nil
}
