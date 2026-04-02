package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"mallow/investment/internal/module/portfolio/command"
	"mallow/investment/internal/module/portfolio/event"
	"mallow/investment/internal/module/transaction/domain"
	"mallow/investment/internal/module/transaction/repository"
)

var tracer = otel.Tracer("investment/transaction")

// Service defines the transaction use-case interface.
type Service interface {
	List(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioTransaction, error)
	Record(ctx context.Context, accountID, userID uuid.UUID, tx event.TransactionRecorded) error
}

type transactionSvc struct {
	repo    repository.Repository
	handler *command.Handler
	logger  *slog.Logger
}

func New(repo repository.Repository, handler *command.Handler, logger *slog.Logger) Service {
	return &transactionSvc{repo: repo, handler: handler, logger: logger.With("component", "transaction.service")}
}

func (s *transactionSvc) List(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioTransaction, error) {
	ctx, span := tracer.Start(ctx, "transaction.List")
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID.String()))
	return s.repo.ListByUserID(ctx, userID, filter)
}

func (s *transactionSvc) Record(ctx context.Context, accountID, userID uuid.UUID, tx event.TransactionRecorded) error {
	ctx, span := tracer.Start(ctx, "transaction.Record")
	defer span.End()
	span.SetAttributes(
		attribute.String("account_id", accountID.String()),
		attribute.String("user_id", userID.String()),
		attribute.String("type", string(tx.Type)),
		attribute.String("symbol", tx.Symbol),
	)
	return s.handler.HandleRecordTransaction(ctx, command.RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: tx,
	})
}
