package service

import (
	"strings"

	"mallow/helm/internal/module/account/domain"
	"mallow/helm/internal/shared"
)

// parseAccountType validates and parses account type string.
func parseAccountType(value string) (domain.AccountType, error) {
	switch strings.ToLower(value) {
	case string(domain.AccountTypeSpot):
		return domain.AccountTypeSpot, nil
	case string(domain.AccountTypeFuturesUSDM):
		return domain.AccountTypeFuturesUSDM, nil
	case string(domain.AccountTypeFuturesCOINM):
		return domain.AccountTypeFuturesCOINM, nil
	case string(domain.AccountTypeUnified):
		return domain.AccountTypeUnified, nil
	case string(domain.AccountTypeOptions):
		return domain.AccountTypeOptions, nil
	default:
		return "", shared.ErrBadRequest.WithDetails("field", "account_type").WithDetails("reason", "invalid value")
	}
}

func normalizeString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

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

func boolPtr(b bool) *bool {
	return &b
}

func maskAccountNumber(n string) string {
	n = strings.TrimSpace(n)
	if len(n) <= 4 {
		return strings.Repeat("*", len(n))
	}
	return "****" + n[len(n)-4:]
}
