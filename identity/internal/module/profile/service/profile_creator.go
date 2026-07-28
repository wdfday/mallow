package service

import (
	"context"
	"errors"

	"mallow/identity/internal/module/profile/domain"
	profiledto "mallow/identity/internal/module/profile/dto"
	"mallow/identity/internal/shared"

	"github.com/google/uuid"
)

// CreateProfile creates a new profile for a user
func (s *profileService) CreateProfile(ctx context.Context, userID string, req profiledto.CreateProfileRequest) (*domain.UserProfile, error) {
	// Parse and validate user ID
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, shared.ErrBadRequest.WithDetails("field", "user_id").WithDetails("reason", "invalid UUID format")
	}

	// Check if profile already exists
	if _, err := s.repo.GetByUserID(ctx, userID); err == nil {
		return nil, shared.ErrConflict.WithDetails("resource", "profile").WithDetails("reason", "profile already exists")
	} else if !errors.Is(err, shared.ErrNotFound) && !errors.Is(err, shared.ErrProfileNotFound) {
		return nil, shared.ErrInternal.WithError(err)
	}

	// Convert request to entity using conversion function
	profile, err := profiledto.FromCreateProfileRequest(req, userUUID)
	if err != nil {
		if appErr := mapDomainValidationErr(err); appErr != nil {
			return nil, appErr
		}
		return nil, shared.ErrInternal.WithError(err)
	}

	// Create profile in repository
	if err := s.repo.Create(ctx, profile); err != nil {
		return nil, shared.WrapRepoErr(err)
	}

	return s.GetProfile(ctx, userID)
}
