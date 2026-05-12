package domain

// AccountType reflects the actual sub-account structure at the exchange.
type AccountType string

const (
	AccountTypeSpot         AccountType = "spot"          // spot equity/crypto (Alpaca, Binance spot)
	AccountTypeFuturesUSDM  AccountType = "futures_usdm"  // USDT-margined perp/futures
	AccountTypeFuturesCOINM AccountType = "futures_coinm" // coin-margined futures
	AccountTypeUnified      AccountType = "unified"       // unified margin pool (OKX UTA, Bybit UTA)
	AccountTypeOptions      AccountType = "options"       // options-only account
)

// SyncStatus represents the sync status of a broker account.
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
