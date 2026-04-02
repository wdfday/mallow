package repository

import (
	"context"
	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
	"time"
)

type Repository interface {
	Create(ctx context.Context, u *domain.User) error
	GetByID(ctx context.Context, id string) (*domain.User, error)
	GetByEmail(ctx context.Context, email string) (*domain.User, error)

	List(ctx context.Context, f domain.ListUsersFilter, p shared.Pagination) (shared.Page[domain.User], error)
	Count(ctx context.Context, f domain.ListUsersFilter) (int64, error)

	Update(ctx context.Context, u *domain.User) error                        // cập nhật toàn bộ (save)
	UpdateColumns(ctx context.Context, id string, cols map[string]any) error // partial update

	SoftDelete(ctx context.Context, id string) error
	Restore(ctx context.Context, id string) error
	HardDelete(ctx context.Context, id string) error

	MarkEmailVerified(ctx context.Context, id string, at time.Time) error
	IncLoginAttempts(ctx context.Context, id string) error
	ResetLoginAttempts(ctx context.Context, id string) error
	SetLockedUntil(ctx context.Context, id string, until *time.Time) error
	UpdateLastLogin(ctx context.Context, id string, at time.Time, ip *string) error

	GetByLinkedAccount(ctx context.Context, provider, providerID string) (*domain.User, error)
	LinkAccount(ctx context.Context, userID string, account domain.LinkedAccount) error
	UnlinkAccount(ctx context.Context, userID, provider, providerID string) error
}
