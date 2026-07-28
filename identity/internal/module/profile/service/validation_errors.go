package service

import (
	"errors"

	"mallow/identity/internal/module/profile/domain"
	"mallow/identity/internal/shared"
)

// domainValidationErrors is the single source of truth mapping a dto-layer conversion
// sentinel (domain.ErrInvalidXxx) to the AppError it should surface as. CreateProfile and
// UpdateProfile share this table instead of each hand-rolling their own errors.Is chain —
// duplicating that chain is exactly how two field cases (income_stability, full_name)
// went missing from one path but not the other.
var domainValidationErrors = []struct {
	err    error
	field  string
	reason string
}{
	{domain.ErrInvalidFullName, "full_name", "cannot be empty"},
	{domain.ErrInvalidIncomeStability, "income_stability", "invalid value"},
	{domain.ErrInvalidRiskTolerance, "risk_tolerance", "invalid value"},
	{domain.ErrInvalidInvestmentHorizon, "investment_horizon", "invalid value"},
	{domain.ErrInvalidInvestmentExperience, "investment_experience", "invalid value"},
	{domain.ErrInvalidBudgetMethod, "budget_method", "invalid value"},
	{domain.ErrInvalidNotificationChannel, "notification_channels", "invalid value"},
	{domain.ErrInvalidReportFrequency, "report_frequency", "invalid value"},
}

// mapDomainValidationErr maps a dto-conversion error to its AppError, or nil if err isn't
// one of the known domain validation sentinels (caller should fall back to shared.ErrInternal).
func mapDomainValidationErr(err error) *shared.AppError {
	for _, m := range domainValidationErrors {
		if errors.Is(err, m.err) {
			return shared.ErrBadRequest.WithDetails("field", m.field).WithDetails("reason", m.reason)
		}
	}
	return nil
}
