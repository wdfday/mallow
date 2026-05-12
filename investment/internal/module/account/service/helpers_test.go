package service

import (
	"testing"

	"mallow/investment/internal/module/account/domain"
	"mallow/investment/internal/shared"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAccountType(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    domain.AccountType
		shouldError bool
	}{
		{"spot", "spot", domain.AccountTypeSpot, false},
		{"spot uppercase", "SPOT", domain.AccountTypeSpot, false},
		{"futures_usdm", "futures_usdm", domain.AccountTypeFuturesUSDM, false},
		{"futures_coinm", "futures_coinm", domain.AccountTypeFuturesCOINM, false},
		{"unified", "unified", domain.AccountTypeUnified, false},
		{"options", "options", domain.AccountTypeOptions, false},
		{"invalid type", "invalid_type", "", true},
		{"empty string", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseAccountType(tt.input)

			if tt.shouldError {
				require.Error(t, err)
				assert.Equal(t, shared.ErrBadRequest.Code, err.(*shared.AppError).Code)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestParseSyncStatus(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    domain.SyncStatus
		shouldError bool
	}{
		{"active", "active", domain.SyncStatusActive, false},
		{"active uppercase", "ACTIVE", domain.SyncStatusActive, false},
		{"error", "error", domain.SyncStatusError, false},
		{"disconnected", "disconnected", domain.SyncStatusDisconnected, false},
		{"invalid status", "invalid_status", "", true},
		{"empty string", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseSyncStatus(tt.input)

			if tt.shouldError {
				require.Error(t, err)
				assert.Equal(t, shared.ErrBadRequest.Code, err.(*shared.AppError).Code)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestNormalizeString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected *string
	}{
		{"normal string", "test", stringPtr("test")},
		{"leading spaces", "  test", stringPtr("test")},
		{"trailing spaces", "test  ", stringPtr("test")},
		{"both spaces", "  test  ", stringPtr("test")},
		{"empty string", "", nil},
		{"only spaces", "   ", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeString(tt.input)

			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				require.NotNil(t, result)
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}

func TestNormalizeNullableString(t *testing.T) {
	tests := []struct {
		name     string
		input    *string
		expected any
	}{
		{"nil input", nil, nil},
		{"normal string", stringPtr("test"), "test"},
		{"string with spaces", stringPtr("  test  "), "test"},
		{"empty string", stringPtr(""), nil},
		{"only spaces", stringPtr("   "), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeNullableString(tt.input)

			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestBoolPtr(t *testing.T) {
	t.Run("true value", func(t *testing.T) {
		result := boolPtr(true)
		require.NotNil(t, result)
		assert.True(t, *result)
	})

	t.Run("false value", func(t *testing.T) {
		result := boolPtr(false)
		require.NotNil(t, result)
		assert.False(t, *result)
	})
}

// Helper function for tests
func stringPtr(s string) *string {
	return &s
}
