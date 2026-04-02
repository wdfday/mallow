package repository

import (
	"context"

	"github.com/google/uuid"
	"mallow/investment/internal/module/watchlist/domain"
)

type Repository interface {
	ListByUserID(ctx context.Context, userID uuid.UUID) ([]domain.WatchlistItem, error)
	Add(ctx context.Context, item *domain.WatchlistItem) error
	Delete(ctx context.Context, userID uuid.UUID, symbol string) error
}
