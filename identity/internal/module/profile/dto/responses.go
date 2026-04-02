package dto

import (
	"time"
)

// ProfileResponse represents the profile details for the authenticated user.
type ProfileResponse struct {
	UserID string `json:"user_id"`

	Occupation                *string    `json:"occupation,omitempty"`
	Industry                  *string    `json:"industry,omitempty"`
	Employer                  *string    `json:"employer,omitempty"`
	DependentsCount           *int       `json:"dependents_count,omitempty"`
	MaritalStatus             *string    `json:"marital_status,omitempty"`
	MonthlyIncomeAvg          *float64   `json:"monthly_income_avg,omitempty"`
	EmergencyFundMonths       *float64   `json:"emergency_fund_months,omitempty"`
	DebtToIncomeRatio         *float64   `json:"debt_to_income_ratio,omitempty"`
	CreditScore               *int       `json:"credit_score,omitempty"`
	IncomeStability           *string    `json:"income_stability,omitempty"`
	RiskTolerance             string     `json:"risk_tolerance"`
	InvestmentHorizon         string     `json:"investment_horizon"`
	InvestmentExperience      string     `json:"investment_experience"`
	BudgetMethod              string     `json:"budget_method"`
	NotificationChannels      []string   `json:"notification_channels"`
	AlertThresholdBudget      *float64   `json:"alert_threshold_budget,omitempty"`
	ReportFrequency           *string    `json:"report_frequency,omitempty"`
	CurrencyPrimary           string     `json:"currency_primary"`
	CurrencySecondary         string     `json:"currency_secondary"`
	PreferredReportDayOfMonth *int       `json:"preferred_report_day_of_month,omitempty"`
	OnboardingCompleted       bool       `json:"onboarding_completed"`
	OnboardingCompletedAt     *time.Time `json:"onboarding_completed_at,omitempty"`
	PrimaryGoal               *string    `json:"primary_goal,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}
