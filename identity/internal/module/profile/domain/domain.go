package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UserProfile maps to the user_profiles table.
type UserProfile struct {
	ID uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`

	// User Relationship
	UserID uuid.UUID `gorm:"type:uuid;uniqueIndex;not null;column:user_id" json:"user_id"`

	// Personal Information — owned here, not on users table
	FullName    string     `gorm:"column:full_name;not null;default:''" json:"full_name"`
	DisplayName *string    `gorm:"column:display_name" json:"display_name,omitempty"`
	DateOfBirth *time.Time `gorm:"column:date_of_birth" json:"date_of_birth,omitempty"`
	AvatarURL   *string    `gorm:"column:avatar_url" json:"avatar_url,omitempty"`

	// Employment Information
	Occupation      *string `gorm:"type:varchar(100);column:occupation" json:"occupation,omitempty"`
	Industry        *string `gorm:"type:varchar(100);column:industry" json:"industry,omitempty"`
	Employer        *string `gorm:"type:varchar(255);column:employer" json:"employer,omitempty"`
	DependentsCount *int    `gorm:"type:int;column:dependents_count" json:"dependents_count,omitempty"`
	MaritalStatus   *string `gorm:"type:varchar(20);column:marital_status" json:"marital_status,omitempty"`

	// Financial Metrics
	MonthlyIncomeAvg    *float64         `gorm:"type:decimal(15,2);column:monthly_income_avg" json:"monthly_income_avg,omitempty"`
	EmergencyFundMonths *float64         `gorm:"type:decimal(4,2);column:emergency_fund_months" json:"emergency_fund_months,omitempty"`
	DebtToIncomeRatio   *float64         `gorm:"type:decimal(5,4);column:debt_to_income_ratio" json:"debt_to_income_ratio,omitempty"`
	CreditScore         *int             `gorm:"type:int;column:credit_score" json:"credit_score,omitempty"`
	IncomeStability     *IncomeStability `gorm:"type:varchar(20);column:income_stability" json:"income_stability,omitempty"`

	// Investment Preferences
	RiskTolerance        RiskTolerance        `gorm:"type:varchar(20);default:'moderate';column:risk_tolerance" json:"risk_tolerance"`
	InvestmentHorizon    InvestmentHorizon    `gorm:"type:varchar(20);default:'medium';column:investment_horizon" json:"investment_horizon"`
	InvestmentExperience InvestmentExperience `gorm:"type:varchar(20);default:'beginner';column:investment_experience" json:"investment_experience"`

	// Budget & Notification Settings
	BudgetMethod         BudgetMethod `gorm:"type:varchar(20);default:'custom';column:budget_method" json:"budget_method"`
	AlertThresholdBudget *float64     `gorm:"type:decimal(5,2);column:alert_threshold_budget" json:"alert_threshold_budget,omitempty"`
	ReportFrequency      *string      `gorm:"type:varchar(20);column:report_frequency" json:"report_frequency,omitempty"`

	// preferences
	CurrencyPrimary           string   `gorm:"default:VND;column:currency_primary" json:"currency_primary"`
	CurrencySecondary         string   `gorm:"default:USD;column:currency_secondary" json:"currency_secondary"`
	Settings                  Settings `gorm:"type:jsonb;serializer:json;column:settings" json:"settings"`
	PreferredReportDayOfMonth *int     `gorm:"type:int;column:preferred_report_day_of_month" json:"preferred_report_day_of_month,omitempty"` // 1–31

	// Onboarding Status
	OnboardingCompleted   bool       `gorm:"column:onboarding_completed" json:"onboarding_completed"`
	OnboardingCompletedAt *time.Time `gorm:"column:onboarding_completed_at" json:"onboarding_completed_at,omitempty"`
	PrimaryGoal           *string    `gorm:"type:text;column:primary_goal" json:"primary_goal,omitempty"`

	CreatedAt time.Time      `gorm:"autoCreateTime;column:created_at" json:"created_at"`
	UpdatedAt time.Time      `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index;column:deleted_at" json:"deleted_at,omitempty"`
}

// TableName matches the database table.
func (UserProfile) TableName() string {
	return "user_profiles"
}

// ========== Helper Methods ==========

// IsOnboardingComplete checks if user has completed onboarding
func (p *UserProfile) IsOnboardingComplete() bool {
	return p.OnboardingCompleted
}

// HasEmergencyFund checks if user has emergency fund set
func (p *UserProfile) HasEmergencyFund() bool {
	return p.EmergencyFundMonths != nil && *p.EmergencyFundMonths > 0
}

// IsConservativeInvestor checks if user has conservative risk tolerance
func (p *UserProfile) IsConservativeInvestor() bool {
	return p.RiskTolerance == RiskToleranceConservative
}

// IsAggressiveInvestor checks if user has aggressive risk tolerance
func (p *UserProfile) IsAggressiveInvestor() bool {
	return p.RiskTolerance == RiskToleranceAggressive
}

// GetRiskLevel returns numeric risk level (1=conservative, 2=moderate, 3=aggressive)
func (p *UserProfile) GetRiskLevel() int {
	switch p.RiskTolerance {
	case RiskToleranceConservative:
		return 1
	case RiskToleranceModerate:
		return 2
	case RiskToleranceAggressive:
		return 3
	default:
		return 2
	}
}

// Settings represents user preferences stored as JSONB
type Settings struct {
	Timezone string `json:"timezone,omitempty"`
	Language string `json:"language,omitempty"`
	Theme    string `json:"theme,omitempty"`

	Notifications struct {
		Email       bool `json:"email,omitempty"`
		Push        bool `json:"push,omitempty"`
		DigestDaily bool `json:"digest_daily,omitempty"`
	} `json:"notifications,omitempty"`

	Appearance struct {
		FontScale float64 `json:"font_scale,omitempty"`
		CompactUI bool    `json:"compact_ui,omitempty"`
	} `json:"appearance,omitempty"`

	// Budget period settings for month module
	Budget struct {
		PeriodType     string `json:"period_type"`      // "calendar_month" or "pay_period"
		PeriodStartDay int    `json:"period_start_day"` // 1-31, only for pay_period
	} `json:"budget,omitempty"`

	Extra map[string]any `json:"extra,omitempty"`
}
