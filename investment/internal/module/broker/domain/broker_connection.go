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

// BrokerConnection represents a user's connection to an external broker/exchange
type BrokerConnection struct {
	ID uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`

	UserID uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	// Broker information
	BrokerType BrokerType             `gorm:"type:varchar(20);not null;index;column:broker_type" json:"broker_type"`
	BrokerName string                 `gorm:"type:varchar(100);not null;column:broker_name" json:"broker_name"` // Display name
	Status     BrokerConnectionStatus `gorm:"type:varchar(20);not null;default:'pending';column:status" json:"status"`

	// Credentials (encrypted)
	APIKey     string  `gorm:"type:text;column:api_key" json:"-"`    // Encrypted
	APISecret  string  `gorm:"type:text;column:api_secret" json:"-"` // Encrypted
	Passphrase *string `gorm:"type:text;column:passphrase" json:"-"` // For OKX

	// Sync settings
	AutoSync       bool       `gorm:"default:true;column:auto_sync" json:"auto_sync"`
	SyncFrequency  int        `gorm:"default:60;column:sync_frequency" json:"sync_frequency"` // minutes
	LastSyncAt     *time.Time `gorm:"column:last_sync_at" json:"last_sync_at,omitempty"`
	LastSyncStatus *string    `gorm:"type:varchar(20);column:last_sync_status" json:"last_sync_status,omitempty"`
	LastSyncError  *string    `gorm:"type:text;column:last_sync_error" json:"last_sync_error,omitempty"`

	// Sync statistics
	TotalSyncs      int `gorm:"default:0;column:total_syncs" json:"total_syncs"`
	SuccessfulSyncs int `gorm:"default:0;column:successful_syncs" json:"successful_syncs"`
	FailedSyncs     int `gorm:"default:0;column:failed_syncs" json:"failed_syncs"`

	// Paper trading flag (persisted for display; does not affect live credentials)
	IsPaper bool `gorm:"default:false;column:is_paper" json:"is_paper"`

	// Additional settings
	SyncAssets       bool `gorm:"default:true;column:sync_assets" json:"sync_assets"`
	SyncTransactions bool `gorm:"default:true;column:sync_transactions" json:"sync_transactions"`
	SyncPrices       bool `gorm:"default:true;column:sync_prices" json:"sync_prices"`
	SyncBalance      bool `gorm:"default:true;column:sync_balance" json:"sync_balance"`

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

// NeedsSync returns true if auto-sync is enabled and enough time has passed
func (bc *BrokerConnection) NeedsSync() bool {
	if !bc.AutoSync {
		return false
	}

	if bc.LastSyncAt == nil {
		return true
	}

	nextSync := bc.LastSyncAt.Add(time.Duration(bc.SyncFrequency) * time.Minute)
	return time.Now().After(nextSync)
}

// UpdateSyncStatus updates the sync status and statistics
func (bc *BrokerConnection) UpdateSyncStatus(success bool, errorMsg *string) {
	now := time.Now()
	bc.LastSyncAt = &now
	bc.TotalSyncs++

	if success {
		bc.SuccessfulSyncs++
		status := "success"
		bc.LastSyncStatus = &status
		bc.LastSyncError = nil
		bc.Status = BrokerConnectionStatusActive
	} else {
		bc.FailedSyncs++
		status := "failed"
		bc.LastSyncStatus = &status
		bc.LastSyncError = errorMsg
		if bc.FailedSyncs >= 3 {
			bc.Status = BrokerConnectionStatusError
		}
	}
}
