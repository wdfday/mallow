package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mallow/helm/internal/infra/engine"
	"mallow/helm/internal/module/hand/domain"
)

func (s *Service) heraldRegister(handID uuid.UUID, b *domain.Hand) {
	if s.herald == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, sym := range b.Symbols {
		req := &engine.RegisterMsg{
			HandId: handID.String(),
			Symbol: sym,
			Script: b.Strategy.Script,
			HelmId: b.HelmID.String(),
		}
		if b.Strategy.Timeframe != "" {
			req.Timeframe = &b.Strategy.Timeframe
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
