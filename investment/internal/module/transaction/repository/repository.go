package repository

import (
	"context"

	"github.com/google/uuid"
	"mallow/investment/internal/module/transaction/domain"
)

type Repository interface {
	ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.PortfolioTransaction, error)
}

type ListFilter struct {
	TxType string
	Symbol string
	Limit  int
	Offset int
}
