package service

import (
	"context"
	"errors"
	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
	"strings"
	"time"
)

// Create creates a new user
func (s *UserService) Create(ctx context.Context, user *domain.User) (*domain.User, error) {
	// Normalize email
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))

	// Check if email already exists
	if _, err := s.repo.GetByEmail(ctx, user.Email); err == nil {
		return nil, shared.ErrConflict.WithDetails("field", "email")
	} else if !errors.Is(err, shared.ErrUserNotFound) {
		if shared.IsAppError(err) {
			return nil, err
		}
		return nil, shared.ErrInternal.WithError(err)
	}

	// Set timestamps
	now := time.Now()
	user.CreatedAt = now
	user.UpdatedAt = now
	user.LastActiveAt = now

	// Create user
	if err := s.repo.Create(ctx, user); err != nil {
		if shared.IsAppError(err) {
			return nil, err
		}
		return nil, shared.ErrInternal.WithError(err)
	}

	// Create default profile for the new user
	// This ensures every user has a profile with sensible defaults
	if s.profileService != nil {
		if _, err := s.profileService.CreateDefaultProfile(ctx, user.ID.String()); err != nil {
			// Log error but don't fail user creation
			// Profile can be created later if needed
			s.logger.Warn(
				"Failed to create default profile for user",
				"user_id", user.ID.String(),
				"error", err,
			)
		}
	}

	return user, nil
}
