package replay

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"gorm.io/gorm"

	cashDomain "mallow/investment/internal/module/cash_flow/domain"
	derivDomain "mallow/investment/internal/module/derivative/domain"
	positionDomain "mallow/investment/internal/module/position/domain"
	snapshotDomain "mallow/investment/internal/module/snapshot/domain"
	txDomain "mallow/investment/internal/module/transaction/domain"

	"mallow/investment/internal/module/portfolio/projection"
	"mallow/investment/internal/module/portfolio/store"
)

// Service rebuilds all projection read models from the event log for a given account.
// Use when a projector failure has left read models stale.
type Service struct {
	db        *gorm.DB
	store     store.EventStore
	projector *projection.Runner
	logger    *slog.Logger
}

func NewService(db *gorm.DB, s store.EventStore, r *projection.Runner, logger *slog.Logger) *Service {
	return &Service{
		db:        db,
		store:     s,
		projector: r,
		logger:    logger.With("component", "replay.service"),
	}
}

// Rebuild clears all projection rows for accountID and replays every event in sequence order.
// Returns the number of events replayed.
func (s *Service) Rebuild(ctx context.Context, accountID uuid.UUID) (int, error) {
	s.logger.Info("starting projection rebuild", "account_id", accountID)

	// 1. Clear all read-model rows for this account (inside a transaction for consistency)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{
			&positionDomain.PortfolioPosition{},
			&txDomain.PortfolioTransaction{},
			&cashDomain.PortfolioCashFlow{},
			&derivDomain.DerivativePosition{},
			&snapshotDomain.PortfolioSnapshot{},
		} {
			if err := tx.Where("account_id = ?", accountID).Delete(model).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("clear projections: %w", err)
	}

	// 2. Load all events (ordered by sequence ASC)
	events, err := s.store.Load(ctx, accountID)
	if err != nil {
		return 0, fmt.Errorf("load events: %w", err)
	}

	// 3. Re-project each event
	replayed := 0
	for _, ev := range events {
		if err := s.projector.Project(ctx, ev); err != nil {
			s.logger.Warn("replay: projector error (skipping)",
				"event_type", ev.EventType,
				"sequence", ev.Sequence,
				"error", err,
			)
		}
		replayed++
	}

	s.logger.Info("projection rebuild complete",
		"account_id", accountID,
		"events_replayed", replayed,
	)
	return replayed, nil
}
