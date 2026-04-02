package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"mallow/investment/internal/module/watchlist/domain"
	"mallow/investment/internal/module/watchlist/repository"
)

var tracer = otel.Tracer("investment/watchlist")

// Service defines the watchlist use-case interface.
type Service interface {
	List(ctx context.Context, userID uuid.UUID) ([]domain.WatchlistItem, error)
	Add(ctx context.Context, item *domain.WatchlistItem) error
	Delete(ctx context.Context, userID uuid.UUID, symbol string) error
}

type watchlistSvc struct {
	repo   repository.Repository
	logger *slog.Logger
}

func New(repo repository.Repository, logger *slog.Logger) Service {
	return &watchlistSvc{repo: repo, logger: logger.With("component", "watchlist.service")}
}

func (s *watchlistSvc) List(ctx context.Context, userID uuid.UUID) ([]domain.WatchlistItem, error) {
	ctx, span := tracer.Start(ctx, "watchlist.List")
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID.String()))
	return s.repo.ListByUserID(ctx, userID)
}

func (s *watchlistSvc) Add(ctx context.Context, item *domain.WatchlistItem) error {
	ctx, span := tracer.Start(ctx, "watchlist.Add")
	defer span.End()
	span.SetAttributes(
		attribute.String("user_id", item.UserID.String()),
		attribute.String("symbol", item.Symbol),
	)
	return s.repo.Add(ctx, item)
}

func (s *watchlistSvc) Delete(ctx context.Context, userID uuid.UUID, symbol string) error {
	ctx, span := tracer.Start(ctx, "watchlist.Delete")
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID.String()), attribute.String("symbol", symbol))
	return s.repo.Delete(ctx, userID, symbol)
}
