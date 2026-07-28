package service

import (
	"context"
	"errors"

	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/shared"

	"github.com/google/uuid"
)

// CreateDefaultProfile creates a default profile for a new user with sensible defaults
func (s *profileService) CreateDefaultProfile(ctx context.Context, userID string) (*domain.UserProfile, error) {
	// Parse and validate user ID
	userUUID, err := uuid.Parse(userID)
	if err != nil {
		return nil, shared.ErrBadRequest.WithDetails("field", "user_id").WithDetails("reason", "invalid UUID format")
	}

	// Check if profile already exists
	if existingProfile, err := s.repo.GetByUserID(ctx, userID); err == nil {
		// Profile already exists, return it
		return existingProfile, nil
	} else if !errors.Is(err, shared.ErrNotFound) && !errors.Is(err, shared.ErrProfileNotFound) {
		return nil, shared.WrapRepoErr(err)
	}

	// Create default profile with sensible defaults for Vietnamese users
	profile := &domain.UserProfile{
		UserID:               userUUID,
		RiskTolerance:        domain.RiskToleranceModerate,
		InvestmentHorizon:    domain.InvestmentHorizonMedium,
		InvestmentExperience: domain.InvestmentExperienceBeginner,
		BudgetMethod:         domain.BudgetMethodCustom,
		AlertThresholdBudget: ptrFloat64(0.80), // Alert when 80% of budget is used
		CurrencyPrimary:      "VND",
		CurrencySecondary:    "USD",
		OnboardingCompleted:  false,
	}

	// Generate UUID for profile
	profile.ID = uuid.New()

	// Create profile in repository
	if err := s.repo.Create(ctx, profile); err != nil {
		return nil, shared.WrapRepoErr(err)
	}

	return s.GetProfile(ctx, userID)
}

// Helper function to create pointer to float64
func ptrFloat64(value float64) *float64 {
	return &value
}
