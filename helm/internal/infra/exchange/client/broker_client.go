package client

import (
	"context"
	"time"

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

	// GetExternalUID returns the exchange's own stable account identifier for
	// these credentials (Binance uid, Bybit uid, Alpaca account id, OKX uid).
	// Used to detect a rotate-key swap onto a different underlying exchange account.
	GetExternalUID(ctx context.Context, creds Credentials) (string, error)
}

// Credentials represents authentication credentials for a broker.
type Credentials struct {
	APIKey      string
	APISecret   string
	IsPaper     bool
	AccountType string // spot | futures_usdm | futures_coinm | unified — used to select the right API endpoint

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

// DetectedAccount is one account type found during auto-detection.
type DetectedAccount struct {
	AccountType string          // spot | futures_usdm | futures_coinm | unified
	CashBalance decimal.Decimal // stablecoin/quote balance for this sub-account
}

// MultiAccountDetector is optionally implemented by brokers whose single API key
// grants access to multiple segregated sub-accounts (e.g. Binance spot + futures).
// Create calls this to auto-detect which sub-accounts exist and create one
// Account + Helm per sub-account.
type MultiAccountDetector interface {
	DetectAccounts(ctx context.Context, creds Credentials) ([]DetectedAccount, error)
}
