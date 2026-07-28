package domain

import (
	"context"
	"time"

	"mallow/identity/internal/shared"
)

// Repository defines data access methods for users.
type Repository interface {
	Create(ctx context.Context, u *User) error
	GetByID(ctx context.Context, id string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	GetByGoogleID(ctx context.Context, googleID string) (*User, error)

	List(ctx context.Context, f ListUsersFilter, p shared.Pagination) (shared.Page[User], error)
	Count(ctx context.Context, f ListUsersFilter) (int64, error)

	Update(ctx context.Context, u *User) error                                // cập nhật toàn bộ (save)
	UpdateColumns(ctx context.Context, id string, cols map[string]any) error // partial update

	SoftDelete(ctx context.Context, id string) error
	Restore(ctx context.Context, id string) error
	HardDelete(ctx context.Context, id string) error

	MarkEmailVerified(ctx context.Context, id string, at time.Time) error
	IncLoginAttempts(ctx context.Context, id string) error
	ResetLoginAttempts(ctx context.Context, id string) error
	SetLockedUntil(ctx context.Context, id string, until *time.Time) error
	UpdateLastLogin(ctx context.Context, id string, at time.Time, ip *string) error

	GetByLinkedAccount(ctx context.Context, provider, providerID string) (*User, error)
	LinkAccount(ctx context.Context, userID string, account LinkedAccount) error
	UnlinkAccount(ctx context.Context, userID, provider, providerID string) error
}
