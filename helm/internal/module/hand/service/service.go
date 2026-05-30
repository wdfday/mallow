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

// helmRegistry is the outbound port to the helm execution runtime.
type helmRegistry interface {
	Get(helmID uuid.UUID) (*runtime.HelmRuntime, error)
}

// heraldClient is the outbound port to the signal herald.
type heraldClient interface {
	Register(ctx context.Context, req *engine.RegisterMsg) (string, error)
	Deregister(ctx context.Context, botID string) error
	ListHands(ctx context.Context) (*engine.HandListResponse, error)
}

// Service is the CRUD + lifecycle layer for hands.
// Business logic (signal handling, order placement, portfolio) lives in runtime.Hand.
type Service struct {
	repo     domain.HandRepo
	registry helmRegistry
	herald   heraldClient // nil when NATS unavailable (dev/test)

	mu sync.RWMutex
	// hands holds all currently active (non-terminal) hands wired into a HelmRuntime.
	hands map[uuid.UUID]*runtime.HandRef
	// terminated holds a snapshot of hands that have been removed from the live map
	// during this process lifetime (killed, released, cascade-stopped, purged). It is
	// also seeded from DB on startup so that hands from previous sessions are visible
	// without hitting the database on every List() call.
	terminated map[uuid.UUID]*domain.Hand
}

func NewService(r domain.HandRepo, registry *runtime.Registry, herald heraldClient) *Service {
	return &Service{
		repo:       r,
		registry:   registry,
		herald:     herald,
		hands:      make(map[uuid.UUID]*runtime.HandRef),
		terminated: make(map[uuid.UUID]*domain.Hand),
	}
}

// HydrateAll loads all persisted hands from the repo and wires them into the
// in-memory cache. Must be called AFTER helm runtimes are registered (i.e.
// after hydrateRuntimes in fx.go) so that registry.Get succeeds for each hand.
func (s *Service) HydrateAll() {
	all := s.repo.All()
	live := 0
	for _, data := range all {
		if data.Status.IsTerminal() {
			// Seed terminated map so List() never needs a DB round-trip after startup.
			s.mu.Lock()
			s.terminated[data.ID] = data
			s.mu.Unlock()
			continue
		}
		bi, err := s.hydrate(data)
		if err != nil {
			slog.Warn("hand hydrate skipped", "id", data.ID, "err", err)
			continue
		}
		s.mu.Lock()
		s.hands[data.ID] = bi
		s.mu.Unlock()
		live++
	}
	slog.Info("hands hydrated", "live", live, "terminated", len(all)-live, "total", len(all))
}

func (s *Service) hydrate(data *domain.Hand) (*runtime.HandRef, error) {
	rt, err := s.registry.Get(data.HelmID)
	if err != nil {
		return nil, fmt.Errorf("no runtime for helm %q: %w", data.HelmID, err)
	}
	strat, tact := runtime.BuildHandComponents(data)
	hand := runtime.NewHand(data.ID, data.HelmID, rt, strat, tact, data.Position.Pyramid, data.Position.MaxUnits, runtime.SignalTTLFor(data), data.Futures, data.OrderType, data.LimitTimeoutSec, data.LimitFallback, data.Risk, data.AllocatedCapital)
	setMeta(hand, data)
	rt.AddHand(hand)
	if data.Status == domain.HandStatusRunning {
		s.heraldRegister(data.ID, data)
	}
	return &runtime.HandRef{Data: data, Runner: hand, Exchange: rt.Exchange}, nil
}

// StartAllHydrated starts the runners of all hydrated hands that have status HandStatusRunning.
// This is called at startup after the orchestrator has reconciled position state.
func (s *Service) StartAllHydrated() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, bi := range s.hands {
		if bi.Data.Status == domain.HandStatusRunning {
			bi.Runner.Start()
		}
	}
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
	// Terminal hands (killed/released) are removed from memory after their lifecycle
	// ends. Do not re-hydrate them — they must not re-enter the runtime.
	if data.Status.IsTerminal() {
		return nil, fmt.Errorf("hand %q is %s — terminal hands cannot be modified", id, data.Status)
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
