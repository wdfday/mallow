package dto

import "time"

// CreateProfileRequest captures data for creating a user profile.
type CreateProfileRequest struct {
	Occupation                *string    `json:"occupation" binding:"omitempty,max=100"`
	Industry                  *string    `json:"industry" binding:"omitempty,max=100"`
	Employer                  *string    `json:"employer" binding:"omitempty,max=255"`
	DependentsCount           *int       `json:"dependents_count" binding:"omitempty,min=0"`
	MaritalStatus             *string    `json:"marital_status" binding:"omitempty,oneof=single married divorced widowed"`
	MonthlyIncomeAvg          *float64   `json:"monthly_income_avg" binding:"omitempty"`
	EmergencyFundMonths       *float64   `json:"emergency_fund_months" binding:"omitempty"`
	DebtToIncomeRatio         *float64   `json:"debt_to_income_ratio" binding:"omitempty"`
	CreditScore               *int       `json:"credit_score" binding:"omitempty"`
	IncomeStability           *string    `json:"income_stability" binding:"omitempty,oneof=stable variable freelance"`
	RiskTolerance             *string    `json:"risk_tolerance" binding:"omitempty,oneof=conservative moderate aggressive"`
	InvestmentHorizon         *string    `json:"investment_horizon" binding:"omitempty,oneof=short medium long"`
	InvestmentExperience      *string    `json:"investment_experience" binding:"omitempty,oneof=beginner intermediate expert"`
	BudgetMethod              *string    `json:"budget_method" binding:"omitempty,oneof=custom zero_based envelope 50_30_20"`
	NotificationChannels      []string   `json:"notification_channels" binding:"omitempty,dive,oneof=email in_app sms"`
	AlertThresholdBudget      *float64   `json:"alert_threshold_budget" binding:"omitempty"`
	ReportFrequency           *string    `json:"report_frequency" binding:"omitempty,oneof=weekly monthly quarterly"`
	CurrencyPrimary           *string    `json:"currency_primary" binding:"omitempty,len=3"`
	CurrencySecondary         *string    `json:"currency_secondary" binding:"omitempty,len=3"`
	PreferredReportDayOfMonth *int       `json:"preferred_report_day_of_month" binding:"omitempty,min=1,max=31"`
	OnboardingCompleted       *bool      `json:"onboarding_completed"`
	OnboardingCompletedAt     *time.Time `json:"onboarding_completed_at" binding:"omitempty"`
	PrimaryGoal               *string    `json:"primary_goal" binding:"omitempty,max=500"`
}

// UpdateProfileRequest captures mutable fields for updating a profile.
type UpdateProfileRequest struct {
	Occupation                *string    `json:"occupation" binding:"omitempty,max=100"`
	Industry                  *string    `json:"industry" binding:"omitempty,max=100"`
	Employer                  *string    `json:"employer" binding:"omitempty,max=255"`
	DependentsCount           *int       `json:"dependents_count" binding:"omitempty,min=0"`
	MaritalStatus             *string    `json:"marital_status" binding:"omitempty,oneof=single married divorced widowed"`
	MonthlyIncomeAvg          *float64   `json:"monthly_income_avg" binding:"omitempty"`
	EmergencyFundMonths       *float64   `json:"emergency_fund_months" binding:"omitempty"`
	DebtToIncomeRatio         *float64   `json:"debt_to_income_ratio" binding:"omitempty"`
	CreditScore               *int       `json:"credit_score" binding:"omitempty"`
	IncomeStability           *string    `json:"income_stability" binding:"omitempty,oneof=stable variable freelance"`
	RiskTolerance             *string    `json:"risk_tolerance" binding:"omitempty,oneof=conservative moderate aggressive"`
	InvestmentHorizon         *string    `json:"investment_horizon" binding:"omitempty,oneof=short medium long"`
	InvestmentExperience      *string    `json:"investment_experience" binding:"omitempty,oneof=beginner intermediate expert"`
	BudgetMethod              *string    `json:"budget_method" binding:"omitempty,oneof=custom zero_based envelope 50_30_20"`
	NotificationChannels      []string   `json:"notification_channels" binding:"omitempty,dive,oneof=email in_app sms"`
	AlertThresholdBudget      *float64   `json:"alert_threshold_budget" binding:"omitempty"`
	ReportFrequency           *string    `json:"report_frequency" binding:"omitempty,oneof=weekly monthly quarterly"`
	CurrencyPrimary           *string    `json:"currency_primary" binding:"omitempty,len=3"`
	CurrencySecondary         *string    `json:"currency_secondary" binding:"omitempty,len=3"`
	PreferredReportDayOfMonth *int       `json:"preferred_report_day_of_month" binding:"omitempty,min=1,max=31"`
	OnboardingCompleted       *bool      `json:"onboarding_completed"`
	OnboardingCompletedAt     *time.Time `json:"onboarding_completed_at" binding:"omitempty"`
	PrimaryGoal               *string    `json:"primary_goal" binding:"omitempty,max=500"`
}
