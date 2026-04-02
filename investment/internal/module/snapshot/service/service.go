package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"mallow/investment/internal/module/portfolio/command"
	"mallow/investment/internal/module/portfolio/event"
	positionDomain "mallow/investment/internal/module/position/domain"
	"mallow/investment/internal/module/snapshot/domain"
	"mallow/investment/internal/module/snapshot/repository"
)

var tracer = otel.Tracer("investment/snapshot")

// Service defines the snapshot use-case interface.
type Service interface {
	List(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioSnapshot, error)
	Trigger(ctx context.Context, accountID, userID uuid.UUID, snapType event.SnapshotType) error
}

// positionQuerier is a minimal interface for fetching active positions.
type positionQuerier interface {
	ListActiveByAccount(ctx context.Context, accountID uuid.UUID) ([]positionDomain.PortfolioPosition, error)
}

type snapshotSvc struct {
	repo            repository.Repository
	commandHandler  *command.Handler
	positionQuerier positionQuerier
	logger          *slog.Logger
}

func New(
	repo repository.Repository,
	commandHandler *command.Handler,
	pq positionQuerier,
	logger *slog.Logger,
) Service {
	return &snapshotSvc{
		repo:            repo,
		commandHandler:  commandHandler,
		positionQuerier: pq,
		logger:          logger.With("component", "snapshot.service"),
	}
}

func (s *snapshotSvc) List(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioSnapshot, error) {
	ctx, span := tracer.Start(ctx, "snapshot.List")
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID.String()))
	return s.repo.ListByUserID(ctx, userID, filter)
}

func (s *snapshotSvc) Trigger(ctx context.Context, accountID, userID uuid.UUID, snapType event.SnapshotType) error {
	ctx, span := tracer.Start(ctx, "snapshot.Trigger")
	defer span.End()
	span.SetAttributes(
		attribute.String("account_id", accountID.String()),
		attribute.String("user_id", userID.String()),
		attribute.String("type", string(snapType)),
	)

	positions, err := s.positionQuerier.ListActiveByAccount(ctx, accountID)
	if err != nil {
		return err
	}

	var totalValue, totalCost, unrealizedPnL, realizedPnL, totalDividends float64
	for _, p := range positions {
		totalValue += p.CurrentValue
		totalCost += p.TotalCost
		unrealizedPnL += p.UnrealizedPnL
		realizedPnL += p.RealizedPnL
		totalDividends += p.TotalDividends
	}

	totalReturn := unrealizedPnL + realizedPnL
	var totalReturnPct float64
	if totalCost > 0 {
		totalReturnPct = (totalReturn / totalCost) * 100
	}

	now := time.Now().UTC()
	payload := event.PortfolioSnapshotTaken{
		SnapshotDate:   now,
		SnapshotType:   snapType,
		TotalValue:     totalValue,
		SpotValue:      totalValue,
		TotalCost:      totalCost,
		UnrealizedPnL:  unrealizedPnL,
		RealizedPnL:    realizedPnL,
		TotalDividends: totalDividends,
		TotalReturn:    totalReturn,
		TotalReturnPct: totalReturnPct,
	}

	return s.commandHandler.HandleTakeSnapshot(ctx, command.TakeSnapshot{
		AccountID:    accountID,
		UserID:       userID,
		SnapshotDate: now,
		SnapshotType: snapType,
		Payload:      payload,
	})
}
