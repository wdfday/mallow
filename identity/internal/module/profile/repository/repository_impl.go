package repository

import (
	"context"
	"errors"
	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/shared"

	"gorm.io/gorm"
)

type gormRepository struct {
	db *gorm.DB
}

// New creates a new profile repository instance.
func New(db *gorm.DB) domain.Repository {
	return &gormRepository{db: db}
}

func (r *gormRepository) GetByUserID(ctx context.Context, userID string) (*domain.UserProfile, error) {
	var profile domain.UserProfile
	if err := r.db.WithContext(ctx).First(&profile, "user_id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, shared.ErrNotFound
		}
		return nil, shared.MapDatabaseError(err)
	}
	return &profile, nil
}

func (r *gormRepository) Create(ctx context.Context, profile *domain.UserProfile) error {
	return shared.MapDatabaseError(r.db.WithContext(ctx).Create(profile).Error)
}

func (r *gormRepository) Update(ctx context.Context, profile *domain.UserProfile) error {
	return shared.MapDatabaseError(r.db.WithContext(ctx).Save(profile).Error)
}

func (r *gormRepository) UpdateColumns(ctx context.Context, userID string, columns map[string]any) error {
	columns["updated_at"] = gorm.Expr("NOW()")
	result := r.db.WithContext(ctx).Model(&domain.UserProfile{}).
		Where("user_id = ?", userID).
		Updates(columns)
	if result.Error != nil {
		return shared.MapDatabaseError(result.Error)
	}
	if result.RowsAffected == 0 {
		return shared.ErrNotFound
	}
	return nil
}
