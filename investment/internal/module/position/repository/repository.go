package repository

import (
	"context"

	"mallow/investment/internal/module/position/domain"

	"github.com/google/uuid"
)

// Repository is the read-only data access interface for spot positions.
type Repository interface {
	ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.PortfolioPosition, error)
	ListByAccountID(ctx context.Context, accountID uuid.UUID, filter ListFilter) ([]domain.PortfolioPosition, error)
	GetBySymbol(ctx context.Context, userID uuid.UUID, symbol string) (*domain.PortfolioPosition, error)
}

type ListFilter struct {
	Status string // "active" | "closed" | "" (all)
}
