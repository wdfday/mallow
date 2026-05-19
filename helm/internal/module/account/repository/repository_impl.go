package repository

import (
	"context"
	"errors"

	"mallow/helm/internal/module/account/domain"
	"mallow/helm/internal/shared"

	"gorm.io/gorm"
)

type gormRepository struct {
	db *gorm.DB
}

// New creates a new account repository instance.
func New(db *gorm.DB) Repository {
	return &gormRepository{db: db}
}

// Migrate runs AutoMigrate for the accounts table.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&domain.Account{})
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
