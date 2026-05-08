package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
)

// heraldClient is the subset of engine.SignalClient used by the service.
type heraldClient interface {
	Register(ctx context.Context, req *engine.RegisterMsg) (string, error)
	Deregister(ctx context.Context, botID string) error
	ListHands(ctx context.Context) (*engine.HandListResponse, error)
}

// Service is the CRUD + lifecycle layer for hands.
// Business logic (signal handling, order placement, portfolio) lives in runtime.Hand.
type Service struct {
	repo     domain.HandRepo
	registry *runtime.Registry
	herald   heraldClient // nil when NATS unavailable (dev/test)

	mu    sync.RWMutex
	hands map[uuid.UUID]*runtime.HandRef
}

func NewService(r domain.HandRepo, registry *runtime.Registry, herald heraldClient) *Service {
	return &Service{
		repo:     r,
		registry: registry,
		herald:   herald,
		hands:    make(map[uuid.UUID]*runtime.HandRef),
	}
}

// HydrateAll loads all persisted hands from the repo and wires them into the
// in-memory cache. Must be called AFTER helm runtimes are registered (i.e.
// after hydrateRuntimes in fx.go) so that registry.Get succeeds for each hand.
func (s *Service) HydrateAll() {
	for _, data := range s.repo.All() {
		bi, err := s.hydrate(data)
		if err != nil {
			slog.Warn("hand hydrate skipped", "id", data.ID, "err", err)
			continue
		}
		s.mu.Lock()
		s.hands[data.ID] = bi
		s.mu.Unlock()
	}
	slog.Info("hands hydrated", "count", len(s.repo.All()))
}

func (s *Service) hydrate(data *domain.Hand) (*runtime.HandRef, error) {
	rt, err := s.registry.Get(data.HelmID)
	if err != nil {
		return nil, fmt.Errorf("no runtime for helm %q: %w", data.HelmID, err)
	}
	strat, tact := runtime.BuildHandComponents(data)
	hand := runtime.NewHand(data.ID, data.HelmID, rt, strat, tact, data.Position.Pyramid, data.Position.MaxUnits)
	setMeta(hand, data)
	rt.AddHand(hand)
	if data.Status == "running" {
		s.heraldRegister(data.ID, data)
		hand.Start()
	}
	return &runtime.HandRef{Data: data, Runner: hand, Exchange: rt.Exchange}, nil
}

func (s *Service) getOrLoad(id uuid.UUID) (*runtime.HandRef, error) {
	s.mu.RLock()
	bi, ok := s.hands[id]
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
	s.hands[id] = bi
	s.mu.Unlock()
	return bi, nil
}
