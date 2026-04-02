package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"mallow/investment/internal/module/derivative/domain"
	"mallow/investment/internal/module/derivative/repository"
)

var tracer = otel.Tracer("investment/derivative")

// Service defines the derivative position use-case interface.
type Service interface {
	List(ctx context.Context, userID uuid.UUID, status string) ([]domain.DerivativePosition, error)
}

type derivativeSvc struct {
	repo   repository.Repository
	logger *slog.Logger
}

func New(repo repository.Repository, logger *slog.Logger) Service {
	return &derivativeSvc{repo: repo, logger: logger.With("component", "derivative.service")}
}

func (s *derivativeSvc) List(ctx context.Context, userID uuid.UUID, status string) ([]domain.DerivativePosition, error) {
	ctx, span := tracer.Start(ctx, "derivative.List")
	defer span.End()
	if status == "" {
		status = "open"
	}
	span.SetAttributes(attribute.String("user_id", userID.String()), attribute.String("status", status))
	return s.repo.ListByUserID(ctx, userID, repository.ListFilter{Status: status})
}
