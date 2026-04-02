package dto

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"mallow/identity/internal/module/profile/domain"
)

func stringPtr(s string) *string {
	return &s
}

func intPtr(i int) *int {
	return &i
}

func floatPtr(f float64) *float64 {
	return &f
}

func boolPtr(b bool) *bool {
	return &b
}

func TestToProfileResponse(t *testing.T) {
	t.Run("converts profile to response", func(t *testing.T) {
		userID := uuid.New()
		now := time.Now()
		stability := domain.IncomeStabilityStable

		profile := &domain.UserProfile{
			ID:                   uuid.New(),
			UserID:               userID,
			Occupation:           stringPtr("Engineer"),
			Industry:             stringPtr("Tech"),
			IncomeStability:      &stability,
			RiskTolerance:        domain.RiskToleranceModerate,
			InvestmentHorizon:    domain.InvestmentHorizonLong,
			InvestmentExperience: domain.InvestmentExperienceIntermediate,
			BudgetMethod:         domain.BudgetMethod50_30_20,
			CurrencyPrimary:      "VND",
			CurrencySecondary:    "USD",
			OnboardingCompleted:  true,
			CreatedAt:            now,
			UpdatedAt:            now,
		}

		response := ToProfileResponse(profile)

		assert.NotNil(t, response)
		assert.Equal(t, userID.String(), response.UserID)
		assert.Equal(t, "Engineer", *response.Occupation)
		assert.Equal(t, "stable", *response.IncomeStability)
		assert.Equal(t, "moderate", response.RiskTolerance)
		assert.True(t, response.OnboardingCompleted)
	})

	t.Run("returns nil for nil input", func(t *testing.T) {
		response := ToProfileResponse(nil)
		assert.Nil(t, response)
	})

	t.Run("handles nil income stability", func(t *testing.T) {
		profile := &domain.UserProfile{
			UserID:          uuid.New(),
			IncomeStability: nil,
			RiskTolerance:   domain.RiskToleranceModerate,
		}

		response := ToProfileResponse(profile)

		assert.Nil(t, response.IncomeStability)
	})
}

func TestFromCreateProfileRequest(t *testing.T) {
	userID := uuid.New()

	t.Run("creates profile with defaults", func(t *testing.T) {
		req := CreateProfileRequest{}

		profile, err := FromCreateProfileRequest(req, userID)

		assert.NoError(t, err)
		assert.NotNil(t, profile)
		assert.Equal(t, userID, profile.UserID)
		assert.Equal(t, domain.RiskToleranceModerate, profile.RiskTolerance)
		assert.Equal(t, domain.InvestmentHorizonMedium, profile.InvestmentHorizon)
		assert.Equal(t, domain.InvestmentExperienceBeginner, profile.InvestmentExperience)
		assert.Equal(t, domain.BudgetMethodCustom, profile.BudgetMethod)
		assert.Equal(t, "VND", profile.CurrencyPrimary)
		assert.Equal(t, "USD", profile.CurrencySecondary)
	})

	t.Run("applies all request fields", func(t *testing.T) {
		req := CreateProfileRequest{
			Occupation:          stringPtr("  Developer  "),
			Industry:            stringPtr("Software"),
			MonthlyIncomeAvg:    floatPtr(10000),
			RiskTolerance:       stringPtr("aggressive"),
			InvestmentHorizon:   stringPtr("long"),
			BudgetMethod:        stringPtr("50_30_20"),
			CurrencyPrimary:     stringPtr("  usd  "),
			OnboardingCompleted: boolPtr(true),
		}

		profile, err := FromCreateProfileRequest(req, userID)

		assert.NoError(t, err)
		assert.Equal(t, "Developer", *profile.Occupation)
		assert.Equal(t, domain.RiskToleranceAggressive, profile.RiskTolerance)
		assert.Equal(t, domain.InvestmentHorizonLong, profile.InvestmentHorizon)
		assert.Equal(t, domain.BudgetMethod50_30_20, profile.BudgetMethod)
		assert.Equal(t, "USD", profile.CurrencyPrimary)
		assert.True(t, profile.OnboardingCompleted)
		assert.NotNil(t, profile.OnboardingCompletedAt)
	})

	t.Run("returns error for invalid risk tolerance", func(t *testing.T) {
		req := CreateProfileRequest{
			RiskTolerance: stringPtr("invalid"),
		}

		profile, err := FromCreateProfileRequest(req, userID)

		assert.Error(t, err)
		assert.Nil(t, profile)
	})

	t.Run("returns error for invalid income stability", func(t *testing.T) {
		req := CreateProfileRequest{
			IncomeStability: stringPtr("invalid"),
		}

		profile, err := FromCreateProfileRequest(req, userID)

		assert.Error(t, err)
		assert.Nil(t, profile)
	})

	t.Run("uses provided onboarding completed at", func(t *testing.T) {
		completedAt := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		req := CreateProfileRequest{
			OnboardingCompleted:   boolPtr(true),
			OnboardingCompletedAt: &completedAt,
		}

		profile, err := FromCreateProfileRequest(req, userID)

		assert.NoError(t, err)
		assert.Equal(t, completedAt, *profile.OnboardingCompletedAt)
	})
}

func TestApplyUpdateProfileRequest(t *testing.T) {
	t.Run("applies employment updates", func(t *testing.T) {
		req := UpdateProfileRequest{
			Occupation:      stringPtr("Senior Engineer"),
			Industry:        stringPtr("Finance"),
			DependentsCount: intPtr(2),
		}

		updates, err := ApplyUpdateProfileRequest(req)

		assert.NoError(t, err)
		assert.Equal(t, "Senior Engineer", updates["occupation"])
		assert.Equal(t, "Finance", updates["industry"])
		assert.Equal(t, 2, updates["dependents_count"])
	})

	t.Run("applies investment preference updates", func(t *testing.T) {
		req := UpdateProfileRequest{
			RiskTolerance:        stringPtr("conservative"),
			InvestmentHorizon:    stringPtr("short"),
			InvestmentExperience: stringPtr("expert"),
		}

		updates, err := ApplyUpdateProfileRequest(req)

		assert.NoError(t, err)
		assert.Equal(t, "conservative", updates["risk_tolerance"])
		assert.Equal(t, "short", updates["investment_horizon"])
		assert.Equal(t, "expert", updates["investment_experience"])
	})

	t.Run("applies budget and currency updates", func(t *testing.T) {
		req := UpdateProfileRequest{
			BudgetMethod:              stringPtr("envelope"),
			AlertThresholdBudget:      floatPtr(0.75),
			CurrencyPrimary:           stringPtr("  eur  "),
			PreferredReportDayOfMonth: intPtr(15),
		}

		updates, err := ApplyUpdateProfileRequest(req)

		assert.NoError(t, err)
		assert.Equal(t, "envelope", updates["budget_method"])
		assert.Equal(t, 0.75, updates["alert_threshold_budget"])
		assert.Equal(t, "EUR", updates["currency_primary"])
		assert.Equal(t, 15, updates["preferred_report_day_of_month"])
	})

	t.Run("sets onboarding timestamp when completed", func(t *testing.T) {
		req := UpdateProfileRequest{
			OnboardingCompleted: boolPtr(true),
		}

		updates, err := ApplyUpdateProfileRequest(req)

		assert.NoError(t, err)
		assert.True(t, updates["onboarding_completed"].(bool))
		assert.NotNil(t, updates["onboarding_completed_at"])
	})

	t.Run("clears nullable string with empty value", func(t *testing.T) {
		req := UpdateProfileRequest{
			Occupation: stringPtr(""),
		}

		updates, err := ApplyUpdateProfileRequest(req)

		assert.NoError(t, err)
		assert.Nil(t, updates["occupation"])
	})

	t.Run("returns error for invalid budget method", func(t *testing.T) {
		req := UpdateProfileRequest{
			BudgetMethod: stringPtr("invalid"),
		}

		updates, err := ApplyUpdateProfileRequest(req)

		assert.Error(t, err)
		assert.Nil(t, updates)
	})
}

// Test enum parsers
func TestParseIncomeStability(t *testing.T) {
	tests := []struct {
		input    string
		expected domain.IncomeStability
		hasError bool
	}{
		{"stable", domain.IncomeStabilityStable, false},
		{"variable", domain.IncomeStabilityVariable, false},
		{"freelance", domain.IncomeStabilityFreelance, false},
		{"STABLE", domain.IncomeStabilityStable, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseIncomeStability(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestParseRiskTolerance(t *testing.T) {
	tests := []struct {
		input    string
		expected domain.RiskTolerance
		hasError bool
	}{
		{"conservative", domain.RiskToleranceConservative, false},
		{"moderate", domain.RiskToleranceModerate, false},
		{"aggressive", domain.RiskToleranceAggressive, false},
		{"CONSERVATIVE", domain.RiskToleranceConservative, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseRiskTolerance(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestParseInvestmentHorizon(t *testing.T) {
	tests := []struct {
		input    string
		expected domain.InvestmentHorizon
		hasError bool
	}{
		{"short", domain.InvestmentHorizonShort, false},
		{"medium", domain.InvestmentHorizonMedium, false},
		{"long", domain.InvestmentHorizonLong, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseInvestmentHorizon(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestParseInvestmentExperience(t *testing.T) {
	tests := []struct {
		input    string
		expected domain.InvestmentExperience
		hasError bool
	}{
		{"beginner", domain.InvestmentExperienceBeginner, false},
		{"intermediate", domain.InvestmentExperienceIntermediate, false},
		{"expert", domain.InvestmentExperienceExpert, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseInvestmentExperience(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestParseBudgetMethod(t *testing.T) {
	tests := []struct {
		input    string
		expected domain.BudgetMethod
		hasError bool
	}{
		{"custom", domain.BudgetMethodCustom, false},
		{"zero_based", domain.BudgetMethodZeroBased, false},
		{"envelope", domain.BudgetMethodEnvelope, false},
		{"50_30_20", domain.BudgetMethod50_30_20, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseBudgetMethod(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestParseReportFrequency(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		hasError bool
	}{
		{"weekly", "weekly", false},
		{"monthly", "monthly", false},
		{"quarterly", "quarterly", false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseReportFrequency(tt.input)
			if tt.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}
