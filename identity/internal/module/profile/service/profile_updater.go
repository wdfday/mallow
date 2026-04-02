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
		// Map domain errors to shared errors
		if err == domain.ErrInvalidIncomeStability {
			return nil, shared.ErrBadRequest.WithDetails("field", "income_stability").WithDetails("reason", "invalid value")
		}
		if err == domain.ErrInvalidRiskTolerance {
			return nil, shared.ErrBadRequest.WithDetails("field", "risk_tolerance").WithDetails("reason", "invalid value")
		}
		if err == domain.ErrInvalidInvestmentHorizon {
			return nil, shared.ErrBadRequest.WithDetails("field", "investment_horizon").WithDetails("reason", "invalid value")
		}
		if err == domain.ErrInvalidInvestmentExperience {
			return nil, shared.ErrBadRequest.WithDetails("field", "investment_experience").WithDetails("reason", "invalid value")
		}
		if err == domain.ErrInvalidBudgetMethod {
			return nil, shared.ErrBadRequest.WithDetails("field", "budget_method").WithDetails("reason", "invalid value")
		}
		if err == domain.ErrInvalidNotificationChannel {
			return nil, shared.ErrBadRequest.WithDetails("field", "notification_channels").WithDetails("reason", "invalid value")
		}
		if err == domain.ErrInvalidReportFrequency {
			return nil, shared.ErrBadRequest.WithDetails("field", "report_frequency").WithDetails("reason", "invalid value")
		}
		return nil, shared.ErrInternal.WithError(err)
	}

	// Apply updates if any
	if len(updates) > 0 {
		if err := s.repo.UpdateColumns(ctx, userID, updates); err != nil {
			if shared.IsAppError(err) {
				return nil, err
			}
			return nil, shared.ErrInternal.WithError(err)
		}
	}

	return s.GetProfile(ctx, userID)
}
