package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
)

// heraldValidate calls engine.validate for all symbols in cfg before the hand
// is persisted. Fails hard when herald is unavailable — a hand with an unvalidated
// strategy must not be created.
func (s *Service) heraldValidate(cfg domain.HandConfig, rt *runtime.HelmRuntime) error {
	if s.herald == nil {
		return fmt.Errorf("herald unavailable: cannot validate strategy before creating hand")
	}
	exchangeName := rt.Exchange.Name()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, sym := range cfg.Symbols {
		req := &engine.RegisterMsg{
			HandId:    "validate",
			Symbol:    sym,
			Exchange:  exchangeName,
			IsFuture:  cfg.Market == domain.MarketTypeFutures,
			Script:    cfg.Strategy.Script,
			HelmId:    cfg.HelmID.String(),
			Timeframe: cfg.Strategy.Timeframe,
		}
		if err := s.herald.Validate(ctx, req); err != nil {
			return fmt.Errorf("strategy validation failed for symbol %q: %w", sym, err)
		}
	}
	return nil
}

func (s *Service) heraldRegister(handID uuid.UUID, b *domain.Hand) error {
	if s.herald == nil {
		return nil
	}
	// Resolve exchange name from the helm's runtime so herald can bind the
	// hand to the correct feed (ledger key = "{exchange}:{symbol}").
	// Failing to resolve means bars will never match — treat as hard error.
	rt, err := s.registry.Get(b.HelmID)
	if err != nil {
		return fmt.Errorf("herald register: could not resolve exchange for helm %q: %w", b.HelmID, err)
	}
	exchangeName := rt.Exchange.Name()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, sym := range b.Symbols {
		req := &engine.RegisterMsg{
			HandId:    handID.String(),
			Symbol:    sym,
			Exchange:  exchangeName,
			IsFuture:  b.Market == domain.MarketTypeFutures,
			Script:    b.Strategy.Script,
			HelmId:    b.HelmID.String(),
			Timeframe: b.Strategy.Timeframe, // required end-to-end
		}
		if ack, err := s.herald.Register(ctx, req); err != nil {
			return fmt.Errorf("herald register failed for symbol %q: %w", sym, err)
		} else {
			slog.Info("herald registered", "hand_id", handID, "symbol", sym, "ack", ack)
		}
	}
	return nil
}

func (s *Service) heraldDeregister(handID uuid.UUID) {
	if s.herald == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.herald.Deregister(ctx, handID.String()); err != nil {
		slog.Warn("herald deregister failed", "hand_id", handID, "err", err)
	}
}

// ReregisterByIDs re-registers specific hands by their string IDs.
// Called after heartbeat returns a non-empty missing list.
func (s *Service) ReregisterByIDs(handIDs []string) {
	if s.herald == nil || len(handIDs) == 0 {
		return
	}
	idSet := make(map[string]bool, len(handIDs))
	for _, id := range handIDs {
		idSet[id] = true
	}
	s.mu.RLock()
	snapshot := make([]*runtime.HandRef, 0, len(handIDs))
	for _, ref := range s.hands {
		if idSet[ref.Data.ID.String()] {
			snapshot = append(snapshot, ref)
		}
	}
	s.mu.RUnlock()
	registered := 0
	for _, ref := range snapshot {
		if err := s.heraldRegister(ref.Data.ID, ref.Data); err != nil {
			slog.Error("herald re-register by IDs: failed", "hand_id", ref.Data.ID, "err", err)
			continue
		}
		registered++
	}
	if registered > 0 {
		slog.Info("herald re-register: by IDs", "count", registered)
	}
}

// DeregisterByIDs deregisters specific hands from herald by their string IDs.
// Called after heartbeat returns a non-empty orphan list — hands herald still
// tracks but helm no longer expects. No local state to clean up; we just send
// the deregister to herald so its registry matches reality.
func (s *Service) DeregisterByIDs(handIDs []string) {
	if s.herald == nil || len(handIDs) == 0 {
		return
	}
	for _, raw := range handIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			slog.Warn("herald orphan deregister: skipping malformed id", "id", raw, "err", err)
			continue
		}
		s.heraldDeregister(id)
	}
	slog.Info("herald deregister orphans: done", "count", len(handIDs))
}

// ReregisterAll re-registers all running hands with herald.
// Called after detecting a herald restart (via engine.ready or heartbeat missing[]).
func (s *Service) ReregisterAll() {
	if s.herald == nil {
		return
	}
	s.mu.RLock()
	snapshot := make([]*runtime.HandRef, 0, len(s.hands))
	for _, ref := range s.hands {
		snapshot = append(snapshot, ref)
	}
	s.mu.RUnlock()

	count := 0
	for _, ref := range snapshot {
		if ref.Data.Status != domain.HandStatusRunning {
			continue
		}
		if err := s.heraldRegister(ref.Data.ID, ref.Data); err != nil {
			slog.Error("herald re-register: failed", "hand_id", ref.Data.ID, "err", err)
			continue
		}
		count++
	}
	if count > 0 {
		slog.Info("herald re-register: all running hands", "count", count)
	}
}
