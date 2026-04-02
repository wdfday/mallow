package domain

import "errors"

var (
	// ErrInvalidIncomeStability is returned when income stability value is invalid
	ErrInvalidIncomeStability = errors.New("invalid income stability")

	// ErrInvalidRiskTolerance is returned when risk tolerance value is invalid
	ErrInvalidRiskTolerance = errors.New("invalid risk tolerance")

	// ErrInvalidInvestmentHorizon is returned when investment horizon value is invalid
	ErrInvalidInvestmentHorizon = errors.New("invalid investment horizon")

	// ErrInvalidInvestmentExperience is returned when investment experience value is invalid
	ErrInvalidInvestmentExperience = errors.New("invalid investment experience")

	// ErrInvalidBudgetMethod is returned when budget method value is invalid
	ErrInvalidBudgetMethod = errors.New("invalid budget method")

	// ErrInvalidNotificationChannel is returned when notification channel value is invalid
	ErrInvalidNotificationChannel = errors.New("invalid notification channel")

	// ErrInvalidReportFrequency is returned when report frequency value is invalid
	ErrInvalidReportFrequency = errors.New("invalid report frequency")

	// ErrProfileAlreadyExists is returned when profile already exists for user
	ErrProfileAlreadyExists = errors.New("profile already exists")

	// ErrProfileNotFound is returned when profile is not found
	ErrProfileNotFound = errors.New("profile not found")

	// ErrInvalidMaritalStatus is returned when marital status is invalid
	ErrInvalidMaritalStatus = errors.New("invalid marital status")
)
