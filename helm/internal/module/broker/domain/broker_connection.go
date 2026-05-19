package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BrokerType represents the type of broker/exchange
type BrokerType string

const (
	BrokerTypeOKX     BrokerType = "okx"     // OKX Exchange (Crypto)
	BrokerTypeBinance BrokerType = "binance" // Binance (Crypto)
	BrokerTypeAlpaca  BrokerType = "alpaca"  // Alpaca (US equities/crypto)
	BrokerTypeBybit   BrokerType = "bybit"   // Bybit (Crypto derivatives)
)

// BrokerConnectionStatus represents the connection status
type BrokerConnectionStatus string

const (
	BrokerConnectionStatusActive       BrokerConnectionStatus = "active"
	BrokerConnectionStatusDisconnected BrokerConnectionStatus = "disconnected"
	BrokerConnectionStatusError        BrokerConnectionStatus = "error"
	BrokerConnectionStatusPending      BrokerConnectionStatus = "pending"
)

// BrokerConnection represents a user's connection to an external broker/exchange.
// Sync-related fields from investment service have been removed — helm does not
// perform portfolio synchronisation.
type BrokerConnection struct {
	ID uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`

	UserID uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	// Broker information
	BrokerType BrokerType             `gorm:"type:varchar(20);not null;index;column:broker_type" json:"broker_type"`
	BrokerName string                 `gorm:"type:varchar(100);not null;column:broker_name" json:"broker_name"`
	Status     BrokerConnectionStatus `gorm:"type:varchar(20);not null;default:'pending';column:status" json:"status"`

	// Credentials (encrypted)
	APIKey     string  `gorm:"type:text;column:api_key" json:"-"`
	APISecret  string  `gorm:"type:text;column:api_secret" json:"-"`
	Passphrase *string `gorm:"type:text;column:passphrase" json:"-"` // For OKX

	// Paper trading flag
	IsPaper bool `gorm:"default:false;column:is_paper" json:"is_paper"`

	// External account info (fetched from broker)
	ExternalAccountID     *string `gorm:"type:varchar(100);column:external_account_id" json:"external_account_id,omitempty"`
	ExternalAccountNumber *string `gorm:"type:varchar(100);column:external_account_number" json:"external_account_number,omitempty"`
	ExternalAccountName   *string `gorm:"type:varchar(255);column:external_account_name" json:"external_account_name,omitempty"`

	// Metadata
	Notes *string `gorm:"type:text;column:notes" json:"notes,omitempty"`

	CreatedAt time.Time      `gorm:"autoCreateTime;column:created_at" json:"created_at"`
	UpdatedAt time.Time      `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index;column:deleted_at" json:"deleted_at,omitempty"`
}

// TableName specifies the database table name
func (*BrokerConnection) TableName() string {
	return "broker_connections"
}

// IsActive returns true if the connection is active
func (bc *BrokerConnection) IsActive() bool {
	return bc.Status == BrokerConnectionStatusActive
}
