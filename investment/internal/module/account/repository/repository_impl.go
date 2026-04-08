package repository

import (
	"context"
	"errors"

	"github.com/shopspring/decimal"
	"mallow/investment/internal/module/account/domain"
	"mallow/investment/internal/shared"

	"gorm.io/gorm"
)

type gormRepository struct {
	db *gorm.DB
}

// New creates a new account repository instance.
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

func base(db *gorm.DB) *gorm.DB {
	return db.Where("deleted_at IS NULL")
}

func (r *gormRepository) GetByID(ctx context.Context, id string) (*domain.Account, error) {
	var account domain.Account
	if err := base(r.db).WithContext(ctx).First(&account, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, shared.ErrNotFound
		}
		return nil, err
	}
	return &account, nil
}

func (r *gormRepository) GetByIDAndUserID(ctx context.Context, id, userID string) (*domain.Account, error) {
	var account domain.Account
	if err := base(r.db).WithContext(ctx).
		First(&account, "id = ? AND user_id = ?", id, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, shared.ErrNotFound
		}
		return nil, err
	}
	return &account, nil
}

func (r *gormRepository) ListByUserID(ctx context.Context, userID string, filters domain.ListAccountsFilter) ([]domain.Account, error) {
	var accounts []domain.Account
	query := r.applyFilters(base(r.db), filters)

	if err := query.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

func (r *gormRepository) CountByUserID(ctx context.Context, userID string, filters domain.ListAccountsFilter) (int64, error) {
	var count int64
	query := r.applyFilters(base(r.db), filters)

	if err := query.WithContext(ctx).
		Model(&domain.Account{}).
		Where("user_id = ?", userID).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *gormRepository) applyFilters(db *gorm.DB, filters domain.ListAccountsFilter) *gorm.DB {
	q := db
	if filters.AccountType != nil {
		q = q.Where("account_type = ?", *filters.AccountType)
	}
	if filters.IsActive != nil {
		q = q.Where("is_active = ?", *filters.IsActive)
	}
	if filters.IsPrimary != nil {
		q = q.Where("is_primary = ?", *filters.IsPrimary)
	}
	if filters.IncludeDeleted {
		q = q.Session(&gorm.Session{}).Unscoped()
	}
	return q
}

func (r *gormRepository) Create(ctx context.Context, account *domain.Account) error {
	return r.db.WithContext(ctx).Create(account).Error
}

func (r *gormRepository) Update(ctx context.Context, account *domain.Account) error {
	return r.db.WithContext(ctx).Save(account).Error
}

func (r *gormRepository) UpdateColumns(ctx context.Context, id string, columns map[string]any) error {
	columns["updated_at"] = gorm.Expr("NOW()")
	result := r.db.WithContext(ctx).Model(&domain.Account{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Updates(columns)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return shared.ErrNotFound
	}
	return nil
}

func (r *gormRepository) SoftDelete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Model(&domain.Account{}).
		Where("id = ? AND deleted_at IS NULL", id).
		Update("deleted_at", gorm.Expr("NOW()"))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return shared.ErrNotFound
	}
	return nil
}

// UpdateBalance updates account balance atomically
// balanceDelta: positive for credit, negative for debit
// This method uses the repository's db connection. For ACID transactions, use UpdateBalanceWithTx instead.
func (r *gormRepository) UpdateBalance(ctx context.Context, accountID string, balanceDelta decimal.Decimal) error {
	return r.UpdateBalanceWithTx(r.db.WithContext(ctx), accountID, balanceDelta)
}

// UpdateBalanceWithTx updates account balance within an existing database transaction
func (r *gormRepository) UpdateBalanceWithTx(tx *gorm.DB, accountID string, balanceDelta decimal.Decimal) error {
	result := tx.Model(&domain.Account{}).
		Where("id = ? AND deleted_at IS NULL", accountID).
		Update("current_balance", gorm.Expr("current_balance + ?", balanceDelta))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return shared.ErrNotFound
	}
	return nil
}

// GetAccountsNeedingSync retrieves accounts that need broker syncing
func (r *gormRepository) GetAccountsNeedingSync(ctx context.Context) ([]*domain.Account, error) {
	var accounts []*domain.Account

	// Query accounts that:
	// 1. Have broker_integration configured (JSONB field is not null)
	// 2. Are active
	// 3. Have investment or crypto_wallet type
	// 4. Need syncing based on frequency
	err := r.db.WithContext(ctx).
		Where("broker_integration IS NOT NULL").
		Where("is_active = ?", true).
		Where("deleted_at IS NULL").
		Where("account_type IN (?)", []string{"investment", "crypto_wallet"}).
		Find(&accounts).Error

	if err != nil {
		return nil, err
	}

	// TODO: Filter accounts that actually need syncing
	// NeedsSync() method is deprecated and moved to broker module
	// For now, return all accounts matching the criteria
	// var needsSync []*domain.Account
	// for _, account := range accounts {
	// 	if account.NeedsSync() {
	// 		needsSync = append(needsSync, account)
	// 	}
	// }

	return accounts, nil
}
