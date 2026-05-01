package dto

import (
	"mallow/identity/internal/module/profile/domain"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ToResponse converts domain UserProfile to ProfileResponse DTO
func ToProfileResponse(profile *domain.UserProfile) *ProfileResponse {
	if profile == nil {
		return nil
	}

	var incomeStability *string
	if profile.IncomeStability != nil {
		val := string(*profile.IncomeStability)
		incomeStability = &val
	}

	return &ProfileResponse{
		UserID:                    profile.UserID.String(),
		FullName:                  profile.FullName,
		DisplayName:               profile.DisplayName,
		DateOfBirth:               profile.DateOfBirth,
		AvatarURL:                 profile.AvatarURL,
		Occupation:                profile.Occupation,
		Industry:                  profile.Industry,
		Employer:                  profile.Employer,
		DependentsCount:           profile.DependentsCount,
		MaritalStatus:             profile.MaritalStatus,
		MonthlyIncomeAvg:          profile.MonthlyIncomeAvg,
		EmergencyFundMonths:       profile.EmergencyFundMonths,
		DebtToIncomeRatio:         profile.DebtToIncomeRatio,
		CreditScore:               profile.CreditScore,
		IncomeStability:           incomeStability,
		RiskTolerance:             string(profile.RiskTolerance),
		InvestmentHorizon:         string(profile.InvestmentHorizon),
		InvestmentExperience:      string(profile.InvestmentExperience),
		BudgetMethod:              string(profile.BudgetMethod),
		AlertThresholdBudget:      profile.AlertThresholdBudget,
		ReportFrequency:           profile.ReportFrequency,
		CurrencyPrimary:           profile.CurrencyPrimary,
		CurrencySecondary:         profile.CurrencySecondary,
		PreferredReportDayOfMonth: profile.PreferredReportDayOfMonth,
		OnboardingCompleted:       profile.OnboardingCompleted,
		OnboardingCompletedAt:     profile.OnboardingCompletedAt,
		PrimaryGoal:               profile.PrimaryGoal,
		CreatedAt:                 profile.CreatedAt,
		UpdatedAt:                 profile.UpdatedAt,
	}
}

// FromCreateProfileRequest converts CreateProfileRequest DTO to UserProfile entity
func FromCreateProfileRequest(req CreateProfileRequest, userID uuid.UUID) (*domain.UserProfile, error) {
	// Create profile with defaults
	profile := &domain.UserProfile{
		UserID:               userID,
		RiskTolerance:        domain.RiskToleranceModerate,
		InvestmentHorizon:    domain.InvestmentHorizonMedium,
		InvestmentExperience: domain.InvestmentExperienceBeginner,
		BudgetMethod:         domain.BudgetMethodCustom,
		AlertThresholdBudget: ptrFloat64(0.80),
		CurrencyPrimary:      "VND",
		CurrencySecondary:    "USD",
	}

	// Generate UUID V7
	profile.ID = uuid.New()

	// Personal Information
	if req.FullName != nil {
		trimmed := strings.TrimSpace(*req.FullName)
		if trimmed != "" {
			profile.FullName = trimmed
		}
	}
	if req.DisplayName != nil {
		profile.DisplayName = trimPointer(req.DisplayName)
	}
	if req.DateOfBirth != nil {
		profile.DateOfBirth = req.DateOfBirth
	}
	if req.AvatarURL != nil {
		profile.AvatarURL = trimPointer(req.AvatarURL)
	}

	// Apply request fields
	if req.Occupation != nil {
		profile.Occupation = trimPointer(req.Occupation)
	}
	if req.Industry != nil {
		profile.Industry = trimPointer(req.Industry)
	}
	if req.Employer != nil {
		profile.Employer = trimPointer(req.Employer)
	}
	if req.DependentsCount != nil {
		profile.DependentsCount = req.DependentsCount
	}
	if req.MaritalStatus != nil {
		profile.MaritalStatus = trimPointer(req.MaritalStatus)
	}
	profile.MonthlyIncomeAvg = req.MonthlyIncomeAvg
	profile.EmergencyFundMonths = req.EmergencyFundMonths
	profile.DebtToIncomeRatio = req.DebtToIncomeRatio
	profile.CreditScore = req.CreditScore

	// Parse and validate enums
	if req.IncomeStability != nil {
		val, err := ParseIncomeStability(*req.IncomeStability)
		if err != nil {
			return nil, err
		}
		profile.IncomeStability = &val
	}
	if req.RiskTolerance != nil {
		val, err := ParseRiskTolerance(*req.RiskTolerance)
		if err != nil {
			return nil, err
		}
		profile.RiskTolerance = val
	}
	if req.InvestmentHorizon != nil {
		val, err := ParseInvestmentHorizon(*req.InvestmentHorizon)
		if err != nil {
			return nil, err
		}
		profile.InvestmentHorizon = val
	}
	if req.InvestmentExperience != nil {
		val, err := ParseInvestmentExperience(*req.InvestmentExperience)
		if err != nil {
			return nil, err
		}
		profile.InvestmentExperience = val
	}
	if req.BudgetMethod != nil {
		val, err := ParseBudgetMethod(*req.BudgetMethod)
		if err != nil {
			return nil, err
		}
		profile.BudgetMethod = val
	}
	if req.AlertThresholdBudget != nil {
		profile.AlertThresholdBudget = req.AlertThresholdBudget
	}
	if req.ReportFrequency != nil {
		val, err := ParseReportFrequency(*req.ReportFrequency)
		if err != nil {
			return nil, err
		}
		profile.ReportFrequency = &val
	}
	if req.CurrencyPrimary != nil {
		profile.CurrencyPrimary = strings.ToUpper(strings.TrimSpace(*req.CurrencyPrimary))
	}
	if req.CurrencySecondary != nil {
		profile.CurrencySecondary = strings.ToUpper(strings.TrimSpace(*req.CurrencySecondary))
	}
	if req.PreferredReportDayOfMonth != nil {
		profile.PreferredReportDayOfMonth = req.PreferredReportDayOfMonth
	}
	if req.OnboardingCompleted != nil {
		profile.OnboardingCompleted = *req.OnboardingCompleted
		if profile.OnboardingCompleted {
			if req.OnboardingCompletedAt != nil {
				profile.OnboardingCompletedAt = req.OnboardingCompletedAt
			} else {
				now := time.Now().UTC()
				profile.OnboardingCompletedAt = &now
			}
		}
	}
	if req.PrimaryGoal != nil {
		profile.PrimaryGoal = trimPointer(req.PrimaryGoal)
	}

	return profile, nil
}

// ApplyUpdateProfileRequest converts UpdateProfileRequest to update map for GORM
func ApplyUpdateProfileRequest(req UpdateProfileRequest) (map[string]any, error) {
	updates := make(map[string]any)

	// Personal Information
	if req.FullName != nil {
		trimmed := strings.TrimSpace(*req.FullName)
		if trimmed == "" {
			return nil, domain.ErrInvalidFullName
		}
		updates["full_name"] = trimmed
	}
	if req.DisplayName != nil {
		updates["display_name"] = normalizeNullableString(req.DisplayName)
	}
	if req.DateOfBirth != nil {
		updates["date_of_birth"] = *req.DateOfBirth
	}
	if req.AvatarURL != nil {
		updates["avatar_url"] = normalizeNullableString(req.AvatarURL)
	}

	// Employment Information
	if req.Occupation != nil {
		updates["occupation"] = normalizeNullableString(req.Occupation)
	}
	if req.Industry != nil {
		updates["industry"] = normalizeNullableString(req.Industry)
	}
	if req.Employer != nil {
		updates["employer"] = normalizeNullableString(req.Employer)
	}
	if req.DependentsCount != nil {
		updates["dependents_count"] = *req.DependentsCount
	}
	if req.MaritalStatus != nil {
		updates["marital_status"] = normalizeNullableString(req.MaritalStatus)
	}

	// Financial Metrics
	if req.MonthlyIncomeAvg != nil {
		updates["monthly_income_avg"] = *req.MonthlyIncomeAvg
	}
	if req.EmergencyFundMonths != nil {
		updates["emergency_fund_months"] = *req.EmergencyFundMonths
	}
	if req.DebtToIncomeRatio != nil {
		updates["debt_to_income_ratio"] = *req.DebtToIncomeRatio
	}
	if req.CreditScore != nil {
		updates["credit_score"] = *req.CreditScore
	}
	if req.IncomeStability != nil {
		val, err := ParseIncomeStability(*req.IncomeStability)
		if err != nil {
			return nil, err
		}
		updates["income_stability"] = string(val)
	}

	// Investment Preferences
	if req.RiskTolerance != nil {
		val, err := ParseRiskTolerance(*req.RiskTolerance)
		if err != nil {
			return nil, err
		}
		updates["risk_tolerance"] = string(val)
	}
	if req.InvestmentHorizon != nil {
		val, err := ParseInvestmentHorizon(*req.InvestmentHorizon)
		if err != nil {
			return nil, err
		}
		updates["investment_horizon"] = string(val)
	}
	if req.InvestmentExperience != nil {
		val, err := ParseInvestmentExperience(*req.InvestmentExperience)
		if err != nil {
			return nil, err
		}
		updates["investment_experience"] = string(val)
	}

	// Budget & Notification Settings
	if req.BudgetMethod != nil {
		val, err := ParseBudgetMethod(*req.BudgetMethod)
		if err != nil {
			return nil, err
		}
		updates["budget_method"] = string(val)
	}
	if req.AlertThresholdBudget != nil {
		updates["alert_threshold_budget"] = *req.AlertThresholdBudget
	}
	if req.ReportFrequency != nil {
		val, err := ParseReportFrequency(*req.ReportFrequency)
		if err != nil {
			return nil, err
		}
		updates["report_frequency"] = val
	}

	// Currency & Report Settings
	if req.CurrencyPrimary != nil {
		updates["currency_primary"] = strings.ToUpper(strings.TrimSpace(*req.CurrencyPrimary))
	}
	if req.CurrencySecondary != nil {
		updates["currency_secondary"] = strings.ToUpper(strings.TrimSpace(*req.CurrencySecondary))
	}
	if req.PreferredReportDayOfMonth != nil {
		updates["preferred_report_day_of_month"] = *req.PreferredReportDayOfMonth
	}

	// Onboarding Status
	if req.OnboardingCompleted != nil {
		updates["onboarding_completed"] = *req.OnboardingCompleted
		if *req.OnboardingCompleted {
			if req.OnboardingCompletedAt != nil {
				updates["onboarding_completed_at"] = *req.OnboardingCompletedAt
			} else {
				updates["onboarding_completed_at"] = time.Now().UTC()
			}
		}
	}
	if req.PrimaryGoal != nil {
		updates["primary_goal"] = normalizeNullableString(req.PrimaryGoal)
	}

	return updates, nil
}

// ========== Enum Parsers and Validators ==========

// ParseIncomeStability validates and parses income stability
func ParseIncomeStability(value string) (domain.IncomeStability, error) {
	switch strings.ToLower(value) {
	case string(domain.IncomeStabilityStable):
		return domain.IncomeStabilityStable, nil
	case string(domain.IncomeStabilityVariable):
		return domain.IncomeStabilityVariable, nil
	case string(domain.IncomeStabilityFreelance):
		return domain.IncomeStabilityFreelance, nil
	default:
		return "", domain.ErrInvalidIncomeStability
	}
}

// ParseRiskTolerance validates and parses risk tolerance
func ParseRiskTolerance(value string) (domain.RiskTolerance, error) {
	switch strings.ToLower(value) {
	case string(domain.RiskToleranceConservative):
		return domain.RiskToleranceConservative, nil
	case string(domain.RiskToleranceModerate):
		return domain.RiskToleranceModerate, nil
	case string(domain.RiskToleranceAggressive):
		return domain.RiskToleranceAggressive, nil
	default:
		return "", domain.ErrInvalidRiskTolerance
	}
}

// ParseInvestmentHorizon validates and parses investment horizon
func ParseInvestmentHorizon(value string) (domain.InvestmentHorizon, error) {
	switch strings.ToLower(value) {
	case string(domain.InvestmentHorizonShort):
		return domain.InvestmentHorizonShort, nil
	case string(domain.InvestmentHorizonMedium):
		return domain.InvestmentHorizonMedium, nil
	case string(domain.InvestmentHorizonLong):
		return domain.InvestmentHorizonLong, nil
	default:
		return "", domain.ErrInvalidInvestmentHorizon
	}
}

// ParseInvestmentExperience validates and parses investment experience
func ParseInvestmentExperience(value string) (domain.InvestmentExperience, error) {
	switch strings.ToLower(value) {
	case string(domain.InvestmentExperienceBeginner):
		return domain.InvestmentExperienceBeginner, nil
	case string(domain.InvestmentExperienceIntermediate):
		return domain.InvestmentExperienceIntermediate, nil
	case string(domain.InvestmentExperienceExpert):
		return domain.InvestmentExperienceExpert, nil
	default:
		return "", domain.ErrInvalidInvestmentExperience
	}
}

// ParseBudgetMethod validates and parses budget method
func ParseBudgetMethod(value string) (domain.BudgetMethod, error) {
	switch strings.ToLower(value) {
	case string(domain.BudgetMethodCustom):
		return domain.BudgetMethodCustom, nil
	case string(domain.BudgetMethodZeroBased):
		return domain.BudgetMethodZeroBased, nil
	case string(domain.BudgetMethodEnvelope):
		return domain.BudgetMethodEnvelope, nil
	case string(domain.BudgetMethod50_30_20):
		return domain.BudgetMethod50_30_20, nil
	default:
		return "", domain.ErrInvalidBudgetMethod
	}
}

// ParseReportFrequency validates and parses report frequency
func ParseReportFrequency(value string) (string, error) {
	switch strings.ToLower(value) {
	case string(domain.ReportFrequencyWeekly):
		return string(domain.ReportFrequencyWeekly), nil
	case string(domain.ReportFrequencyMonthly):
		return string(domain.ReportFrequencyMonthly), nil
	case string(domain.ReportFrequencyQuarterly):
		return string(domain.ReportFrequencyQuarterly), nil
	default:
		return "", domain.ErrInvalidReportFrequency
	}
}

// ========== Helper Functions ==========

// trimPointer trims string and returns nil if empty
func trimPointer(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// normalizeNullableString normalizes pointer to string
func normalizeNullableString(value *string) any {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

// ptrFloat64 returns a pointer to a float64 value
func ptrFloat64(value float64) *float64 {
	return &value
}
