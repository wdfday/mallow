package repository

import (
	"context"

	"github.com/shopspring/decimal"
	"mallow/investment/internal/module/account/domain"

	"gorm.io/gorm"
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
	CountByUserID(ctx context.Context, userID string, filters domain.ListAccountsFilter) (int64, error)

	// UpdateBalance updates account balance atomically (for ACID transactions)
	UpdateBalance(ctx context.Context, accountID string, balanceDelta decimal.Decimal) error

	// UpdateBalanceWithTx updates account balance within an existing database transaction
	UpdateBalanceWithTx(tx *gorm.DB, accountID string, balanceDelta decimal.Decimal) error

	// Broker sync methods
	GetAccountsNeedingSync(ctx context.Context) ([]*domain.Account, error)
}
