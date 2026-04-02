package repository

import (
	"context"

	"github.com/google/uuid"
	"mallow/investment/internal/module/snapshot/domain"
)

type Repository interface {
	ListByUserID(ctx context.Context, userID uuid.UUID, filter ListFilter) ([]domain.PortfolioSnapshot, error)
}

type ListFilter struct {
	SnapshotType string // "daily" | "weekly" | "monthly" | "manual" | "" (all)
	Limit        int
	Offset       int
}
