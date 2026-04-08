package client

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BrokerClient is the interface that all broker clients must implement
type BrokerClient interface {
	// Authenticate authenticates with the broker and returns an access token
	Authenticate(ctx context.Context, credentials Credentials) (*AuthResponse, error)

	// RefreshToken refreshes the access token
	RefreshToken(ctx context.Context, refreshToken string) (*AuthResponse, error)

	// GetPortfolio retrieves the user's portfolio/account balance
	GetPortfolio(ctx context.Context, accessToken string) (*Portfolio, error)

	// GetPositions retrieves the user's current positions
	GetPositions(ctx context.Context, accessToken string) ([]Position, error)

	// GetTransactions retrieves transaction history
	GetTransactions(ctx context.Context, accessToken string, startDate, endDate time.Time) ([]Transaction, error)

	// GetMarketPrice retrieves current market price for a symbol
	GetMarketPrice(ctx context.Context, symbol string) (*MarketPrice, error)

	// GetBatchMarketPrices retrieves prices for multiple symbols
	GetBatchMarketPrices(ctx context.Context, symbols []string) (map[string]*MarketPrice, error)
}

// Credentials represents authentication credentials for a broker
type Credentials struct {
	// Common fields
	APIKey    string
	APISecret string
	IsPaper   bool

	// OKX specific
	Passphrase *string
}

// AuthResponse represents the response from authentication
type AuthResponse struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int // seconds
	ExpiresAt    time.Time
	TokenType    string
}

// Portfolio represents a user's portfolio balance
type Portfolio struct {
	TotalValue      decimal.Decimal            // Total portfolio value in base currency
	TotalCost       decimal.Decimal            // Total cost basis
	UnrealizedGain  decimal.Decimal            // Unrealized P&L
	RealizedGain    decimal.Decimal            // Realized P&L from closed positions
	TotalDividends  decimal.Decimal            // Total dividends received
	CashBalance     decimal.Decimal            // Available cash
	Currency        string                     // Base currency (VND, USD, etc.)
	AssetAllocation map[string]decimal.Decimal // Asset type -> value
	LastUpdated     time.Time
}

// Position represents a single position in the portfolio
type Position struct {
	Symbol             string
	Name               string
	AssetType          string // stock, crypto, etc.
	Quantity           decimal.Decimal
	AverageCostPerUnit decimal.Decimal
	CurrentPrice       decimal.Decimal
	CurrentValue       decimal.Decimal
	UnrealizedGain     decimal.Decimal
	UnrealizedGainPct  float64
	Currency           string
	Exchange           string
	Sector             *string
	Industry           *string
	ExternalID         string // Broker's internal ID
	LastUpdated        time.Time
}

// Transaction represents a broker transaction
type Transaction struct {
	ExternalID      string
	TransactionType string // buy, sell, dividend, fee, deposit, withdrawal, etc.
	Symbol          string
	Quantity        decimal.Decimal
	Price           decimal.Decimal
	Amount          decimal.Decimal
	Fee             decimal.Decimal
	Commission      decimal.Decimal
	Tax             decimal.Decimal
	Currency        string
	TransactionDate time.Time
	SettlementDate  *time.Time
	Status          string
	Notes           string

	// Banking-specific fields
	AccountNumber  string          // External account number from bank
	ReferenceCode  string          // Bank reference number
	RunningBalance decimal.Decimal // Balance after transaction
}

// MarketPrice represents current market price for an asset
type MarketPrice struct {
	Symbol      string
	Price       decimal.Decimal
	Change      decimal.Decimal
	ChangePct   float64
	Volume      float64
	Currency    string
	LastUpdated time.Time
}

// SyncResult represents the result of a sync operation
type SyncResult struct {
	ConnectionID       uuid.UUID
	Success            bool
	SyncedAt           time.Time
	AssetsCount        int
	TransactionsCount  int
	UpdatedPricesCount int
	BalanceUpdated     bool
	Error              *string
	Details            map[string]interface{}
}

// BankingBrokerClient extends BrokerClient for banking-specific operations
type BankingBrokerClient interface {
	BrokerClient

	// GetBankAccounts retrieves all bank accounts from the broker
	GetBankAccounts(ctx context.Context, accessToken string) ([]BankAccount, error)

	// GetAccountTransactions retrieves transactions for a specific account
	GetAccountTransactions(ctx context.Context, accessToken string, accountNumber string, startDate, endDate time.Time) ([]Transaction, error)
}

// BankAccount represents a bank account from banking API
type BankAccount struct {
	AccountNumber     string
	AccountHolderName string
	BankCode          string
	BankName          string
	Balance           decimal.Decimal // Accumulated balance
	LastTransaction   *time.Time      // Last transaction time
	IsActive          bool
}
