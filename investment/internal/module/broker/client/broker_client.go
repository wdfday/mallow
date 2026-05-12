package client

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// BrokerClient is the interface that all broker clients must implement.
// All data methods receive Credentials directly — none of these brokers use OAuth,
// so there is no separate "token" step.
type BrokerClient interface {
	// Validate checks that the credentials are accepted by the broker.
	Validate(ctx context.Context, creds Credentials) error

	// GetPortfolio retrieves the user's portfolio/account balance.
	GetPortfolio(ctx context.Context, creds Credentials) (*Portfolio, error)

	// GetPositions retrieves the user's current positions.
	GetPositions(ctx context.Context, creds Credentials) ([]Position, error)

	// GetTransactions retrieves transaction history.
	GetTransactions(ctx context.Context, creds Credentials, startDate, endDate time.Time) ([]Transaction, error)

	// GetMarketPrice retrieves current market price for a symbol.
	GetMarketPrice(ctx context.Context, symbol string) (*MarketPrice, error)

	// GetBatchMarketPrices retrieves prices for multiple symbols.
	GetBatchMarketPrices(ctx context.Context, symbols []string) (map[string]*MarketPrice, error)
}

// Credentials represents authentication credentials for a broker.
type Credentials struct {
	APIKey    string
	APISecret string
	IsPaper   bool

	// OKX specific
	Passphrase *string
}

// Portfolio represents a user's portfolio balance.
type Portfolio struct {
	TotalValue      decimal.Decimal
	TotalCost       decimal.Decimal
	UnrealizedGain  decimal.Decimal
	RealizedGain    decimal.Decimal
	TotalDividends  decimal.Decimal
	CashBalance     decimal.Decimal
	Currency        string
	AssetAllocation map[string]decimal.Decimal
	LastUpdated     time.Time
}

// Position represents a single position in the portfolio.
type Position struct {
	Symbol             string
	Name               string
	AssetType          string
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
	ExternalID         string
	LastUpdated        time.Time
}

// Transaction represents a broker transaction.
type Transaction struct {
	ExternalID      string
	TransactionType string
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
}

// MarketPrice represents current market price for an asset.
type MarketPrice struct {
	Symbol      string
	Price       decimal.Decimal
	Change      decimal.Decimal
	ChangePct   float64
	Volume      float64
	Currency    string
	LastUpdated time.Time
}

// SyncResult represents the result of a sync operation.
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
