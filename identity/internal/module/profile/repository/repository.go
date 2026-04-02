package repository

import (
	"context"

	"mallow/identity/internal/module/profile/domain"
)

// Repository defines data access methods for user profiles.
type Repository interface {
	GetByUserID(ctx context.Context, userID string) (*domain.UserProfile, error)
	Create(ctx context.Context, profile *domain.UserProfile) error
	Update(ctx context.Context, profile *domain.UserProfile) error
	UpdateColumns(ctx context.Context, userID string, columns map[string]any) error
}
