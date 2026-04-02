package domain

// AccountType represents the type of account.
type AccountType string

const (
	AccountTypeCash         AccountType = "cash"
	AccountTypeBank         AccountType = "bank" // debit card 1 : 1 with bank account
	AccountTypeSavings      AccountType = "savings"
	AccountTypeCreditCard   AccountType = "credit_card"
	AccountTypeInvestment   AccountType = "investment"
	AccountTypeCryptoWallet AccountType = "crypto_wallet"
)

// SyncStatus represents the sync status for Open Banking.
type SyncStatus string

const (
	SyncStatusActive       SyncStatus = "active"
	SyncStatusError        SyncStatus = "error"
	SyncStatusDisconnected SyncStatus = "disconnected"
)

type Currency string

const (
	CurrencyVND Currency = "VND"
	CurrencyUSD Currency = "USD"
	CurrencyEUR Currency = "EUR"
	CurrencyJPY Currency = "JPY"
)
