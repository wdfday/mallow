package domain

import "context"

// Repository defines data access methods for user profiles.
type Repository interface {
	GetByUserID(ctx context.Context, userID string) (*UserProfile, error)
	Create(ctx context.Context, profile *UserProfile) error
	Update(ctx context.Context, profile *UserProfile) error
	UpdateColumns(ctx context.Context, userID string, columns map[string]any) error
}
