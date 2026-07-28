package service

import (
	"context"

	"mallow/identity/internal/module/profile/domain"
	profiledto "mallow/identity/internal/module/profile/dto"
	"mallow/identity/internal/shared"
)

// UpdateProfile updates a user's profile
func (s *profileService) UpdateProfile(ctx context.Context, userID string, req profiledto.UpdateProfileRequest) (*domain.UserProfile, error) {
	// Convert request to updates using conversion function
	updates, err := profiledto.ApplyUpdateProfileRequest(req)
	if err != nil {
		if appErr := mapDomainValidationErr(err); appErr != nil {
			return nil, appErr
		}
		return nil, shared.ErrInternal.WithError(err)
	}

	// Apply updates if any
	if len(updates) > 0 {
		if err := s.repo.UpdateColumns(ctx, userID, updates); err != nil {
			return nil, shared.WrapRepoErr(err)
		}
	}

	return s.GetProfile(ctx, userID)
}
