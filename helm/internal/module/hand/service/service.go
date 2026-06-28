package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
)

// registry is the outbound port to the helm execution runtime.
type registry interface {
	Get(helmID uuid.UUID) (*runtime.HelmRuntime, error)
	All() []*runtime.HelmRuntime
}

// Service is the CRUD + lifecycle layer for hands.
// Business logic (signal handling, order placement, portfolio) lives in runtime.Hand.
// Live hand state is owned by HelmRuntime.hands — Service delegates runtime ops via
// registry.Get(helmID) → HelmRuntime methods.
type Service struct {
	repo     domain.HandRepo
	registry registry
}

func NewService(r domain.HandRepo, registry *runtime.Registry) *Service {
	return &Service{
		repo:     r,
		registry: registry,
	}
}

// HydrateAll loads all persisted hands from the repo and wires them into the runtime.
// Must be called AFTER helm runtimes are registered so that registry.Get succeeds.
func (s *Service) HydrateAll() {
	all := s.repo.All()
	live, skipped := 0, 0
	for _, data := range all {
		if data.Status.IsTerminal() {
			skipped++
			continue
		}
		if err := s.hydrateOne(data); err != nil {
			slog.Warn("hand hydrate skipped", "id", data.ID, "err", err)
			continue
		}
		live++
	}
	slog.Info("hands hydrated", "live", live, "terminal_in_db", skipped, "total", len(all))
}

func (s *Service) hydrateOne(data *domain.Hand) error {
	rt, err := s.registry.Get(data.HelmID)
	if err != nil {
		return fmt.Errorf("no runtime for helm %q: %w", data.HelmID, err)
	}
	strat, tact := runtime.BuildHandComponents(data)
	hand := runtime.NewHand(data.ID, data.HelmID, rt, strat, tact, data.Position.Pyramid, data.Position.MaxUnits, runtime.SignalTTLFor(data), data.Futures, data.OrderType, data.LimitTimeoutSec, data.LimitFallback, data.Guard, data.AllocatedCapital)
	setMeta(hand, data)
	rt.AddHand(hand, data)
	if data.Status == domain.HandStatusRunning {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := rt.RegisterHandAll(ctx, data.ID.String(), data.HelmID.String(), data.Symbols, data.Strategy.Script, data.Strategy.Timeframe, data.Market == domain.MarketTypeFutures); err != nil {
			slog.Warn("herald re-register on hydrate failed", "hand_id", data.ID, "err", err)
		}
	}
	return nil
}

// SymbolsByExchange returns the deduplicated symbols per exchange across all live hands.
func (s *Service) SymbolsByExchange() map[exchange.Exchange][]string {
	seen := make(map[exchange.Exchange]map[string]struct{})
	for _, rt := range s.registry.All() {
		ex, syms := rt.SymbolsByExchange()
		if len(syms) == 0 {
			continue
		}
		if seen[ex] == nil {
			seen[ex] = make(map[string]struct{})
		}
		for _, sym := range syms {
			seen[ex][sym] = struct{}{}
		}
	}
	out := make(map[exchange.Exchange][]string, len(seen))
	for ex, syms := range seen {
		list := make([]string, 0, len(syms))
		for sym := range syms {
			list = append(list, sym)
		}
		out[ex] = list
	}
	return out
}

// StartAllHydrated starts runners of all hydrated hands with status HandStatusRunning.
func (s *Service) StartAllHydrated() {
	for _, rt := range s.registry.All() {
		rt.StartRunning()
	}
}

// RunningHandCount returns the number of running hands across all runtimes.
func (s *Service) RunningHandCount() int {
	n := 0
	for _, rt := range s.registry.All() {
		n += len(rt.RunningHandIDs())
	}
	return n
}
