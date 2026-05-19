package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/module/hand/domain"
	"mallow/helm/internal/runtime"
)

func (s *Service) heraldRegister(handID uuid.UUID, b *domain.Hand) {
	if s.herald == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, sym := range b.Symbols {
		req := &engine.RegisterMsg{
			HandId:    handID.String(),
			Symbol:    sym,
			Script:    b.Strategy.Script,
			HelmId:    b.HelmID.String(),
			Timeframe: b.Strategy.Timeframe, // required end-to-end
		}
		if ack, err := s.herald.Register(ctx, req); err != nil {
			slog.Warn("herald register failed", "hand_id", handID, "symbol", sym, "err", err)
		} else {
			slog.Info("herald registered", "hand_id", handID, "symbol", sym, "ack", ack)
		}
	}
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
	for _, ref := range snapshot {
		s.heraldRegister(ref.Data.ID, ref.Data)
	}
	if len(snapshot) > 0 {
		slog.Info("herald re-register: by IDs", "count", len(snapshot))
	}
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
		s.heraldRegister(ref.Data.ID, ref.Data)
		count++
	}
	if count > 0 {
		slog.Info("herald re-register: all running hands", "count", count)
	}
}
