package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"mallow/investment/internal/module/position/domain"
	"mallow/investment/internal/module/position/repository"
)

var tracer = otel.Tracer("investment/position")

// Service defines the position use-case interface.
type Service interface {
	List(ctx context.Context, userID uuid.UUID, statusFilter string) ([]domain.PortfolioPosition, error)
	ListActive(ctx context.Context, userID uuid.UUID) ([]domain.PortfolioPosition, error)
	ListActiveByAccount(ctx context.Context, accountID uuid.UUID) ([]domain.PortfolioPosition, error)
	GetBySymbol(ctx context.Context, userID uuid.UUID, symbol string) (*domain.PortfolioPosition, error)
}

type positionSvc struct {
	repo   repository.Repository
	logger *slog.Logger
}

func New(repo repository.Repository, logger *slog.Logger) Service {
	return &positionSvc{repo: repo, logger: logger.With("component", "position.service")}
}

func (s *positionSvc) List(ctx context.Context, userID uuid.UUID, statusFilter string) ([]domain.PortfolioPosition, error) {
	ctx, span := tracer.Start(ctx, "position.List")
	defer span.End()
	if statusFilter == "" {
		statusFilter = "active"
	}
	span.SetAttributes(attribute.String("user_id", userID.String()), attribute.String("status", statusFilter))
	return s.repo.ListByUserID(ctx, userID, repository.ListFilter{Status: statusFilter})
}

func (s *positionSvc) ListActive(ctx context.Context, userID uuid.UUID) ([]domain.PortfolioPosition, error) {
	return s.List(ctx, userID, "active")
}

func (s *positionSvc) ListActiveByAccount(ctx context.Context, accountID uuid.UUID) ([]domain.PortfolioPosition, error) {
	ctx, span := tracer.Start(ctx, "position.ListActiveByAccount")
	defer span.End()
	span.SetAttributes(attribute.String("account_id", accountID.String()))
	return s.repo.ListByAccountID(ctx, accountID, repository.ListFilter{Status: "active"})
}

func (s *positionSvc) GetBySymbol(ctx context.Context, userID uuid.UUID, symbol string) (*domain.PortfolioPosition, error) {
	ctx, span := tracer.Start(ctx, "position.GetBySymbol")
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID.String()), attribute.String("symbol", symbol))
	return s.repo.GetBySymbol(ctx, userID, symbol)
}
