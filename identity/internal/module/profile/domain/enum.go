package domain

// RiskTolerance represents the user's risk tolerance level.
type RiskTolerance string

const (
	RiskToleranceConservative RiskTolerance = "conservative"
	RiskToleranceModerate     RiskTolerance = "moderate"
	RiskToleranceAggressive   RiskTolerance = "aggressive"
)

// InvestmentHorizon represents investment duration preference.
type InvestmentHorizon string

const (
	InvestmentHorizonShort  InvestmentHorizon = "short"
	InvestmentHorizonMedium InvestmentHorizon = "medium"
	InvestmentHorizonLong   InvestmentHorizon = "long"
)

// InvestmentExperience represents the user's experience level.
type InvestmentExperience string

const (
	InvestmentExperienceBeginner     InvestmentExperience = "beginner"
	InvestmentExperienceIntermediate InvestmentExperience = "intermediate"
	InvestmentExperienceExpert       InvestmentExperience = "expert"
)

// BudgetMethod represents budgeting approach preference.
type BudgetMethod string

const (
	BudgetMethodZeroBased BudgetMethod = "zero_based"
	BudgetMethodEnvelope  BudgetMethod = "envelope"
	BudgetMethod50_30_20  BudgetMethod = "50_30_20"
	BudgetMethodCustom    BudgetMethod = "custom"
)

// ReportFrequency represents reporting cadence.
type ReportFrequency string

const (
	ReportFrequencyWeekly    ReportFrequency = "weekly"
	ReportFrequencyMonthly   ReportFrequency = "monthly"
	ReportFrequencyQuarterly ReportFrequency = "quarterly"
)

type IncomeStability string

const (
	IncomeStabilityStable    IncomeStability = "stable"    // lương cứng, biên chế, dev full-time
	IncomeStabilityVariable  IncomeStability = "variable"  // lương + bonus, sales
	IncomeStabilityFreelance IncomeStability = "freelance" // thu nhập tự do, không cố định
)
