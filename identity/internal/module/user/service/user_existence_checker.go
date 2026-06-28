package service

import (
	"context"
	"errors"
	"mallow/identity/internal/shared"
	"strings"
	"time"
)

// ExistsByEmail checks if user exists by email
func (s *UserService) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	_, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, shared.ErrUserNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// MarkEmailVerified marks user email as verified
func (s *UserService) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
	return s.repo.MarkEmailVerified(ctx, id, at)
}

// IncLoginAttempts increments login attempts
func (s *UserService) IncLoginAttempts(ctx context.Context, id string) error {
	return s.repo.IncLoginAttempts(ctx, id)
}

// ResetLoginAttempts resets login attempts
func (s *UserService) ResetLoginAttempts(ctx context.Context, id string) error {
	return s.repo.ResetLoginAttempts(ctx, id)
}

// SetLockedUntil sets account lock time
func (s *UserService) SetLockedUntil(ctx context.Context, id string, until *time.Time) error {
	return s.repo.SetLockedUntil(ctx, id, until)
}
