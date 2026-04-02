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
		{
			name:        "valid cash",
			input:       "cash",
			expected:    domain.AccountTypeCash,
			shouldError: false,
		},
		{
			name:        "valid cash uppercase",
			input:       "CASH",
			expected:    domain.AccountTypeCash,
			shouldError: false,
		},
		{
			name:        "valid bank",
			input:       "bank",
			expected:    domain.AccountTypeBank,
			shouldError: false,
		},
		{
			name:        "valid savings",
			input:       "savings",
			expected:    domain.AccountTypeSavings,
			shouldError: false,
		},
		{
			name:        "valid credit_card",
			input:       "credit_card",
			expected:    domain.AccountTypeCreditCard,
			shouldError: false,
		},
		{
			name:        "valid investment",
			input:       "investment",
			expected:    domain.AccountTypeInvestment,
			shouldError: false,
		},
		{
			name:        "valid crypto_wallet",
			input:       "crypto_wallet",
			expected:    domain.AccountTypeCryptoWallet,
			shouldError: false,
		},
		{
			name:        "invalid type",
			input:       "invalid_type",
			expected:    "",
			shouldError: true,
		},
		{
			name:        "empty string",
			input:       "",
			expected:    "",
			shouldError: true,
		},
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
		{
			name:        "valid active",
			input:       "active",
			expected:    domain.SyncStatusActive,
			shouldError: false,
		},
		{
			name:        "valid active uppercase",
			input:       "ACTIVE",
			expected:    domain.SyncStatusActive,
			shouldError: false,
		},
		{
			name:        "valid error",
			input:       "error",
			expected:    domain.SyncStatusError,
			shouldError: false,
		},
		{
			name:        "valid disconnected",
			input:       "disconnected",
			expected:    domain.SyncStatusDisconnected,
			shouldError: false,
		},
		{
			name:        "invalid status",
			input:       "invalid_status",
			expected:    "",
			shouldError: true,
		},
		{
			name:        "empty string",
			input:       "",
			expected:    "",
			shouldError: true,
		},
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
		{
			name:     "normal string",
			input:    "test",
			expected: stringPtr("test"),
		},
		{
			name:     "string with leading spaces",
			input:    "  test",
			expected: stringPtr("test"),
		},
		{
			name:     "string with trailing spaces",
			input:    "test  ",
			expected: stringPtr("test"),
		},
		{
			name:     "string with both leading and trailing spaces",
			input:    "  test  ",
			expected: stringPtr("test"),
		},
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:     "only spaces",
			input:    "   ",
			expected: nil,
		},
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
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "normal string",
			input:    stringPtr("test"),
			expected: "test",
		},
		{
			name:     "string with spaces",
			input:    stringPtr("  test  "),
			expected: "test",
		},
		{
			name:     "empty string",
			input:    stringPtr(""),
			expected: nil,
		},
		{
			name:     "only spaces",
			input:    stringPtr("   "),
			expected: nil,
		},
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
