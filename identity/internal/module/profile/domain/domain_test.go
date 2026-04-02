package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func floatPtr(f float64) *float64 {
	return &f
}

func TestUserProfile_TableName(t *testing.T) {
	profile := UserProfile{}
	assert.Equal(t, "user_profiles", profile.TableName())
}

func TestUserProfile_IsOnboardingComplete(t *testing.T) {
	t.Run("onboarding is complete", func(t *testing.T) {
		profile := &UserProfile{OnboardingCompleted: true}
		assert.True(t, profile.IsOnboardingComplete())
	})

	t.Run("onboarding is not complete", func(t *testing.T) {
		profile := &UserProfile{OnboardingCompleted: false}
		assert.False(t, profile.IsOnboardingComplete())
	})
}

func TestUserProfile_HasEmergencyFund(t *testing.T) {
	t.Run("has emergency fund", func(t *testing.T) {
		months := 3.0
		profile := &UserProfile{EmergencyFundMonths: &months}
		assert.True(t, profile.HasEmergencyFund())
	})

	t.Run("no emergency fund - nil", func(t *testing.T) {
		profile := &UserProfile{EmergencyFundMonths: nil}
		assert.False(t, profile.HasEmergencyFund())
	})

	t.Run("no emergency fund - zero", func(t *testing.T) {
		months := 0.0
		profile := &UserProfile{EmergencyFundMonths: &months}
		assert.False(t, profile.HasEmergencyFund())
	})

	t.Run("no emergency fund - negative", func(t *testing.T) {
		months := -1.0
		profile := &UserProfile{EmergencyFundMonths: &months}
		assert.False(t, profile.HasEmergencyFund())
	})
}

func TestUserProfile_IsConservativeInvestor(t *testing.T) {
	t.Run("is conservative", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceConservative}
		assert.True(t, profile.IsConservativeInvestor())
	})

	t.Run("is not conservative - moderate", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceModerate}
		assert.False(t, profile.IsConservativeInvestor())
	})

	t.Run("is not conservative - aggressive", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceAggressive}
		assert.False(t, profile.IsConservativeInvestor())
	})
}

func TestUserProfile_IsAggressiveInvestor(t *testing.T) {
	t.Run("is aggressive", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceAggressive}
		assert.True(t, profile.IsAggressiveInvestor())
	})

	t.Run("is not aggressive - conservative", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceConservative}
		assert.False(t, profile.IsAggressiveInvestor())
	})

	t.Run("is not aggressive - moderate", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceModerate}
		assert.False(t, profile.IsAggressiveInvestor())
	})
}

func TestUserProfile_GetRiskLevel(t *testing.T) {
	t.Run("conservative returns 1", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceConservative}
		assert.Equal(t, 1, profile.GetRiskLevel())
	})

	t.Run("moderate returns 2", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceModerate}
		assert.Equal(t, 2, profile.GetRiskLevel())
	})

	t.Run("aggressive returns 3", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskToleranceAggressive}
		assert.Equal(t, 3, profile.GetRiskLevel())
	})

	t.Run("unknown defaults to 2", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: RiskTolerance("unknown")}
		assert.Equal(t, 2, profile.GetRiskLevel())
	})

	t.Run("empty defaults to 2", func(t *testing.T) {
		profile := &UserProfile{RiskTolerance: ""}
		assert.Equal(t, 2, profile.GetRiskLevel())
	})
}

func TestUserProfile_Fields(t *testing.T) {
	t.Run("profile with all fields", func(t *testing.T) {
		userID := uuid.New()
		occupation := "Software Engineer"
		industry := "Technology"
		income := 50000.0
		creditScore := 750

		profile := &UserProfile{
			ID:                   uuid.New(),
			UserID:               userID,
			Occupation:           &occupation,
			Industry:             &industry,
			MonthlyIncomeAvg:     &income,
			CreditScore:          &creditScore,
			RiskTolerance:        RiskToleranceModerate,
			InvestmentHorizon:    InvestmentHorizonLong,
			InvestmentExperience: InvestmentExperienceIntermediate,
			BudgetMethod:         BudgetMethod50_30_20,
			CurrencyPrimary:      "VND",
			CurrencySecondary:    "USD",
			OnboardingCompleted:  true,
		}

		assert.Equal(t, userID, profile.UserID)
		assert.Equal(t, "Software Engineer", *profile.Occupation)
		assert.Equal(t, "Technology", *profile.Industry)
		assert.Equal(t, 50000.0, *profile.MonthlyIncomeAvg)
		assert.Equal(t, 750, *profile.CreditScore)
		assert.Equal(t, RiskToleranceModerate, profile.RiskTolerance)
		assert.Equal(t, InvestmentHorizonLong, profile.InvestmentHorizon)
		assert.Equal(t, InvestmentExperienceIntermediate, profile.InvestmentExperience)
		assert.Equal(t, BudgetMethod50_30_20, profile.BudgetMethod)
	})
}

// Test enum constants
func TestRiskTolerance_Constants(t *testing.T) {
	assert.Equal(t, RiskTolerance("conservative"), RiskToleranceConservative)
	assert.Equal(t, RiskTolerance("moderate"), RiskToleranceModerate)
	assert.Equal(t, RiskTolerance("aggressive"), RiskToleranceAggressive)
}

func TestInvestmentHorizon_Constants(t *testing.T) {
	assert.Equal(t, InvestmentHorizon("short"), InvestmentHorizonShort)
	assert.Equal(t, InvestmentHorizon("medium"), InvestmentHorizonMedium)
	assert.Equal(t, InvestmentHorizon("long"), InvestmentHorizonLong)
}

func TestInvestmentExperience_Constants(t *testing.T) {
	assert.Equal(t, InvestmentExperience("beginner"), InvestmentExperienceBeginner)
	assert.Equal(t, InvestmentExperience("intermediate"), InvestmentExperienceIntermediate)
	assert.Equal(t, InvestmentExperience("expert"), InvestmentExperienceExpert)
}

func TestBudgetMethod_Constants(t *testing.T) {
	assert.Equal(t, BudgetMethod("zero_based"), BudgetMethodZeroBased)
	assert.Equal(t, BudgetMethod("envelope"), BudgetMethodEnvelope)
	assert.Equal(t, BudgetMethod("50_30_20"), BudgetMethod50_30_20)
	assert.Equal(t, BudgetMethod("custom"), BudgetMethodCustom)
}

func TestReportFrequency_Constants(t *testing.T) {
	assert.Equal(t, ReportFrequency("weekly"), ReportFrequencyWeekly)
	assert.Equal(t, ReportFrequency("monthly"), ReportFrequencyMonthly)
	assert.Equal(t, ReportFrequency("quarterly"), ReportFrequencyQuarterly)
}

func TestIncomeStability_Constants(t *testing.T) {
	assert.Equal(t, IncomeStability("stable"), IncomeStabilityStable)
	assert.Equal(t, IncomeStability("variable"), IncomeStabilityVariable)
	assert.Equal(t, IncomeStability("freelance"), IncomeStabilityFreelance)
}

func TestSettings_Struct(t *testing.T) {
	settings := Settings{
		Timezone: "Asia/Ho_Chi_Minh",
		Language: "vi",
		Theme:    "dark",
		Extra: map[string]any{
			"custom_field": "value",
		},
	}
	settings.Notifications.Email = true
	settings.Notifications.Push = true
	settings.Notifications.DigestDaily = false
	settings.Appearance.FontScale = 1.2
	settings.Appearance.CompactUI = true
	settings.Budget.PeriodType = "calendar_month"
	settings.Budget.PeriodStartDay = 1

	assert.Equal(t, "Asia/Ho_Chi_Minh", settings.Timezone)
	assert.Equal(t, "vi", settings.Language)
	assert.Equal(t, "dark", settings.Theme)
	assert.True(t, settings.Notifications.Email)
	assert.True(t, settings.Notifications.Push)
	assert.False(t, settings.Notifications.DigestDaily)
	assert.Equal(t, 1.2, settings.Appearance.FontScale)
	assert.True(t, settings.Appearance.CompactUI)
	assert.Equal(t, "calendar_month", settings.Budget.PeriodType)
	assert.Equal(t, 1, settings.Budget.PeriodStartDay)
	assert.Equal(t, "value", settings.Extra["custom_field"])
}
