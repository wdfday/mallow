package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func d(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }
func di(i int64) decimal.Decimal  { return decimal.NewFromInt(i) }

func TestAccount_TableName(t *testing.T) {
	account := Account{}
	assert.Equal(t, "accounts", account.TableName())
}

func TestAccount_UpdateBalance(t *testing.T) {
	tests := []struct {
		name            string
		initialBalance  decimal.Decimal
		amount          decimal.Decimal
		expectedBalance decimal.Decimal
	}{
		{
			name:            "add positive amount",
			initialBalance:  di(10000000),
			amount:          di(5000000),
			expectedBalance: di(15000000),
		},
		{
			name:            "subtract amount (negative value)",
			initialBalance:  di(10000000),
			amount:          di(-3000000),
			expectedBalance: di(7000000),
		},
		{
			name:            "add zero",
			initialBalance:  di(10000000),
			amount:          decimal.Zero,
			expectedBalance: di(10000000),
		},
		{
			name:            "result in negative balance",
			initialBalance:  di(5000000),
			amount:          di(-8000000),
			expectedBalance: di(-3000000),
		},
		{
			name:            "add to zero balance",
			initialBalance:  decimal.Zero,
			amount:          di(1000000),
			expectedBalance: di(1000000),
		},
		{
			name:            "add decimal amounts",
			initialBalance:  d(1000.50),
			amount:          d(500.25),
			expectedBalance: d(1500.75),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				CurrentBalance: tt.initialBalance,
			}

			account.UpdateBalance(tt.amount)

			assert.True(t, tt.expectedBalance.Equal(account.CurrentBalance),
				"expected %s got %s", tt.expectedBalance, account.CurrentBalance)
		})
	}
}

func TestAccount_Structure(t *testing.T) {
	t.Run("create account with all fields", func(t *testing.T) {
		userID := uuid.New()
		accountID := uuid.New()
		brokerConnectionID := uuid.New()
		institutionName := "Test Bank"
		availableBalance := d(8000000)
		accountNumberMasked := "****1234"
		accountNumberEncrypted := "encrypted_data"
		lastSyncedAt := time.Now()
		syncStatus := SyncStatusActive
		syncErrorMessage := "no errors"

		account := Account{
			ID:                     accountID,
			UserID:                 userID,
			AccountName:            "Test Account",
			AccountType:            AccountTypeBank,
			InstitutionName:        &institutionName,
			CurrentBalance:         di(10000000),
			AvailableBalance:       &availableBalance,
			Currency:               CurrencyVND,
			AccountNumberMasked:    &accountNumberMasked,
			AccountNumberEncrypted: &accountNumberEncrypted,
			IsActive:               true,
			IsPrimary:              true,
			IncludeInNetWorth:      true,
			IsAutoSync:             true,
			LastSyncedAt:           &lastSyncedAt,
			SyncStatus:             &syncStatus,
			SyncErrorMessage:       &syncErrorMessage,
			BrokerConnectionID:     &brokerConnectionID,
			CreatedAt:              time.Now(),
			UpdatedAt:              time.Now(),
		}

		assert.Equal(t, accountID, account.ID)
		assert.Equal(t, userID, account.UserID)
		assert.Equal(t, "Test Account", account.AccountName)
		assert.Equal(t, AccountTypeBank, account.AccountType)
		assert.Equal(t, institutionName, *account.InstitutionName)
		assert.True(t, di(10000000).Equal(account.CurrentBalance))
		assert.True(t, d(8000000).Equal(*account.AvailableBalance))
		assert.Equal(t, CurrencyVND, account.Currency)
		assert.Equal(t, accountNumberMasked, *account.AccountNumberMasked)
		assert.Equal(t, accountNumberEncrypted, *account.AccountNumberEncrypted)
		assert.True(t, account.IsActive)
		assert.True(t, account.IsPrimary)
		assert.True(t, account.IncludeInNetWorth)
		assert.True(t, account.IsAutoSync)
		assert.NotNil(t, account.LastSyncedAt)
		assert.Equal(t, SyncStatusActive, *account.SyncStatus)
		assert.Equal(t, brokerConnectionID, *account.BrokerConnectionID)
	})

	t.Run("create minimal account", func(t *testing.T) {
		userID := uuid.New()

		account := Account{
			UserID:         userID,
			AccountName:    "Cash Account",
			AccountType:    AccountTypeCash,
			CurrentBalance: decimal.Zero,
			Currency:       CurrencyVND,
			IsActive:       true,
		}

		assert.Equal(t, userID, account.UserID)
		assert.Equal(t, "Cash Account", account.AccountName)
		assert.Equal(t, AccountTypeCash, account.AccountType)
		assert.True(t, account.CurrentBalance.IsZero())
		assert.Equal(t, CurrencyVND, account.Currency)
		assert.True(t, account.IsActive)
		assert.Nil(t, account.InstitutionName)
		assert.Nil(t, account.AvailableBalance)
		assert.Nil(t, account.AccountNumberMasked)
	})
}

func TestAccountType_Constants(t *testing.T) {
	tests := []struct {
		name        string
		accountType AccountType
		expected    string
	}{
		{"cash", AccountTypeCash, "cash"},
		{"bank", AccountTypeBank, "bank"},
		{"savings", AccountTypeSavings, "savings"},
		{"credit_card", AccountTypeCreditCard, "credit_card"},
		{"investment", AccountTypeInvestment, "investment"},
		{"crypto_wallet", AccountTypeCryptoWallet, "crypto_wallet"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.accountType))
		})
	}
}

func TestSyncStatus_Constants(t *testing.T) {
	tests := []struct {
		name       string
		syncStatus SyncStatus
		expected   string
	}{
		{"active", SyncStatusActive, "active"},
		{"error", SyncStatusError, "error"},
		{"disconnected", SyncStatusDisconnected, "disconnected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.syncStatus))
		})
	}
}

func TestCurrency_Constants(t *testing.T) {
	tests := []struct {
		name     string
		currency Currency
		expected string
	}{
		{"VND", CurrencyVND, "VND"},
		{"USD", CurrencyUSD, "USD"},
		{"EUR", CurrencyEUR, "EUR"},
		{"JPY", CurrencyJPY, "JPY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.currency))
		})
	}
}

func TestAccount_BooleanFlags(t *testing.T) {
	t.Run("IsActive flag", func(t *testing.T) {
		activeAccount := Account{IsActive: true}
		assert.True(t, activeAccount.IsActive)

		inactiveAccount := Account{IsActive: false}
		assert.False(t, inactiveAccount.IsActive)
	})

	t.Run("IsPrimary flag", func(t *testing.T) {
		primaryAccount := Account{IsPrimary: true}
		assert.True(t, primaryAccount.IsPrimary)

		nonPrimaryAccount := Account{IsPrimary: false}
		assert.False(t, nonPrimaryAccount.IsPrimary)
	})

	t.Run("IncludeInNetWorth flag", func(t *testing.T) {
		includedAccount := Account{IncludeInNetWorth: true}
		assert.True(t, includedAccount.IncludeInNetWorth)

		excludedAccount := Account{IncludeInNetWorth: false}
		assert.False(t, excludedAccount.IncludeInNetWorth)
	})

	t.Run("IsAutoSync flag", func(t *testing.T) {
		autoSyncAccount := Account{IsAutoSync: true}
		assert.True(t, autoSyncAccount.IsAutoSync)

		manualAccount := Account{IsAutoSync: false}
		assert.False(t, manualAccount.IsAutoSync)
	})
}

func TestAccount_Timestamps(t *testing.T) {
	now := time.Now()
	account := Account{
		CreatedAt: now,
		UpdatedAt: now,
	}

	assert.Equal(t, now.Unix(), account.CreatedAt.Unix())
	assert.Equal(t, now.Unix(), account.UpdatedAt.Unix())
}

func TestAccount_NullableFields(t *testing.T) {
	t.Run("all nullable fields are nil", func(t *testing.T) {
		account := Account{}

		assert.Nil(t, account.InstitutionName)
		assert.Nil(t, account.AvailableBalance)
		assert.Nil(t, account.AccountNumberMasked)
		assert.Nil(t, account.AccountNumberEncrypted)
		assert.Nil(t, account.LastSyncedAt)
		assert.Nil(t, account.SyncStatus)
		assert.Nil(t, account.SyncErrorMessage)
		assert.Nil(t, account.BrokerConnectionID)
	})

	t.Run("set nullable fields", func(t *testing.T) {
		institutionName := "Test Bank"
		availableBalance := d(5000000)
		accountNumberMasked := "****5678"
		accountNumberEncrypted := "encrypted"
		lastSyncedAt := time.Now()
		syncStatus := SyncStatusActive
		syncErrorMessage := "error message"
		brokerConnectionID := uuid.New()

		account := Account{
			InstitutionName:        &institutionName,
			AvailableBalance:       &availableBalance,
			AccountNumberMasked:    &accountNumberMasked,
			AccountNumberEncrypted: &accountNumberEncrypted,
			LastSyncedAt:           &lastSyncedAt,
			SyncStatus:             &syncStatus,
			SyncErrorMessage:       &syncErrorMessage,
			BrokerConnectionID:     &brokerConnectionID,
		}

		require.NotNil(t, account.InstitutionName)
		assert.Equal(t, institutionName, *account.InstitutionName)

		require.NotNil(t, account.AvailableBalance)
		assert.True(t, d(5000000).Equal(*account.AvailableBalance))

		require.NotNil(t, account.AccountNumberMasked)
		assert.Equal(t, accountNumberMasked, *account.AccountNumberMasked)

		require.NotNil(t, account.AccountNumberEncrypted)
		assert.Equal(t, accountNumberEncrypted, *account.AccountNumberEncrypted)

		require.NotNil(t, account.LastSyncedAt)
		assert.Equal(t, lastSyncedAt.Unix(), account.LastSyncedAt.Unix())

		require.NotNil(t, account.SyncStatus)
		assert.Equal(t, syncStatus, *account.SyncStatus)

		require.NotNil(t, account.SyncErrorMessage)
		assert.Equal(t, syncErrorMessage, *account.SyncErrorMessage)

		require.NotNil(t, account.BrokerConnectionID)
		assert.Equal(t, brokerConnectionID, *account.BrokerConnectionID)
	})
}

func TestAccount_DifferentAccountTypes(t *testing.T) {
	userID := uuid.New()

	tests := []struct {
		name        string
		accountType AccountType
	}{
		{"cash account", AccountTypeCash},
		{"bank account", AccountTypeBank},
		{"savings account", AccountTypeSavings},
		{"credit card", AccountTypeCreditCard},
		{"investment account", AccountTypeInvestment},
		{"crypto wallet", AccountTypeCryptoWallet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := Account{
				UserID:      userID,
				AccountName: tt.name,
				AccountType: tt.accountType,
				Currency:    CurrencyVND,
			}

			assert.Equal(t, tt.accountType, account.AccountType)
		})
	}
}

func TestAccount_MultipleBalanceUpdates(t *testing.T) {
	account := &Account{
		CurrentBalance: di(10000000),
	}

	account.UpdateBalance(di(2000000))
	assert.True(t, di(12000000).Equal(account.CurrentBalance))

	account.UpdateBalance(di(-5000000))
	assert.True(t, di(7000000).Equal(account.CurrentBalance))

	account.UpdateBalance(di(3000000))
	assert.True(t, di(10000000).Equal(account.CurrentBalance))

	account.UpdateBalance(di(-10000000))
	assert.True(t, account.CurrentBalance.IsZero())
}
