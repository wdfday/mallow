package repository

import (
	"context"

	"github.com/google/uuid"
	"mallow/investment/internal/module/derivative/domain"
)

type Repository interface {
	ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.DerivativePosition, error)
}

type ListFilter struct {
	Status string // "open" | "closed" | "" (all)
}
