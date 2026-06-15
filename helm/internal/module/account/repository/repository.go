package repository

import (
	"context"

	"mallow/helm/internal/module/account/domain"
)

// Repository defines data access methods for accounts.
type Repository interface {
	GetByID(ctx context.Context, id string) (*domain.Account, error)
	GetByIDAndUserID(ctx context.Context, id, userID string) (*domain.Account, error)
	ListByUserID(ctx context.Context, userID string, filters domain.ListAccountsFilter) ([]domain.Account, error)
	Create(ctx context.Context, account *domain.Account) error
	Update(ctx context.Context, account *domain.Account) error
	UpdateColumns(ctx context.Context, id string, columns map[string]any) error
	SoftDelete(ctx context.Context, id string) error
	HardDelete(ctx context.Context, id string) error
	CountByUserID(ctx context.Context, userID string, filters domain.ListAccountsFilter) (int64, error)
}
