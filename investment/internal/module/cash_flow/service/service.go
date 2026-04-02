package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"mallow/investment/internal/module/cash_flow/domain"
	"mallow/investment/internal/module/cash_flow/repository"
)

var tracer = otel.Tracer("investment/cash_flow")

// Service defines the cash flow use-case interface.
type Service interface {
	List(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioCashFlow, error)
}

type cashFlowSvc struct {
	repo   repository.Repository
	logger *slog.Logger
}

func New(repo repository.Repository, logger *slog.Logger) Service {
	return &cashFlowSvc{repo: repo, logger: logger.With("component", "cash_flow.service")}
}

func (s *cashFlowSvc) List(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioCashFlow, error) {
	ctx, span := tracer.Start(ctx, "cash_flow.List")
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID.String()))
	return s.repo.ListByUserID(ctx, userID, filter)
}
