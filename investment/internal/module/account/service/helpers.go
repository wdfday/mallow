package service

import (
	"strings"

	"mallow/investment/internal/module/account/domain"
	"mallow/investment/internal/shared"
)

// parseAccountType validates and parses account type string
func parseAccountType(value string) (domain.AccountType, error) {
	switch strings.ToLower(value) {
	case string(domain.AccountTypeCash):
		return domain.AccountTypeCash, nil
	case string(domain.AccountTypeBank):
		return domain.AccountTypeBank, nil
	case string(domain.AccountTypeSavings):
		return domain.AccountTypeSavings, nil
	case string(domain.AccountTypeCreditCard):
		return domain.AccountTypeCreditCard, nil
	case string(domain.AccountTypeInvestment):
		return domain.AccountTypeInvestment, nil
	case string(domain.AccountTypeCryptoWallet):
		return domain.AccountTypeCryptoWallet, nil
	default:
		return "", shared.ErrBadRequest.WithDetails("field", "account_type").WithDetails("reason", "invalid value")
	}
}

// parseSyncStatus validates and parses sync status string
func parseSyncStatus(value string) (domain.SyncStatus, error) {
	switch strings.ToLower(value) {
	case string(domain.SyncStatusActive):
		return domain.SyncStatusActive, nil
	case string(domain.SyncStatusError):
		return domain.SyncStatusError, nil
	case string(domain.SyncStatusDisconnected):
		return domain.SyncStatusDisconnected, nil
	default:
		return "", shared.ErrBadRequest.WithDetails("field", "sync_status").WithDetails("reason", "invalid value")
	}
}

// normalizeString trims and returns nil if empty
func normalizeString(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// normalizeNullableString normalizes pointer to string, returning nil if empty
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

// boolPtr returns a pointer to a bool value
func boolPtr(b bool) *bool {
	return &b
}

// maskAccountNumber masks all but the last 4 characters of an account number.
func maskAccountNumber(n string) string {
	n = strings.TrimSpace(n)
	if len(n) <= 4 {
		return strings.Repeat("*", len(n))
	}
	return "****" + n[len(n)-4:]
}
