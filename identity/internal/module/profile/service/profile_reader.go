package service

import (
	"context"

	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/shared"

	"github.com/google/uuid"
)

// GetProfile retrieves a user's profile
func (s *profileService) GetProfile(ctx context.Context, userID string) (*domain.UserProfile, error) {
	// Validate user ID
	if _, err := uuid.Parse(userID); err != nil {
		return nil, shared.ErrBadRequest.WithDetails("field", "user_id").WithDetails("reason", "invalid UUID format")
	}

	profile, err := s.repo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, shared.WrapRepoErr(err)
	}
	return profile, nil
}
