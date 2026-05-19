package domain

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Account represents a broker sub-account (spot, futures, unified, options).
type Account struct {
	ID     uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index;column:user_id" json:"user_id"`

	AccountName     string      `gorm:"type:varchar(255);not null;column:account_name" json:"account_name"`
	AccountType     AccountType `gorm:"type:varchar(50);not null;column:account_type" json:"account_type"`
	InstitutionName *string     `gorm:"type:varchar(255);column:institution_name" json:"institution_name,omitempty"`
	Currency        Currency    `gorm:"type:varchar(3);not null;default:'USD';column:currency" json:"currency"`

	IsActive          bool `gorm:"default:true;column:is_active" json:"is_active"`
	IncludeInNetWorth bool `gorm:"default:true;column:include_in_net_worth" json:"include_in_net_worth"`

	IsAutoSync       bool        `gorm:"default:false;column:is_auto_sync" json:"is_auto_sync"`
	LastSyncedAt     *time.Time  `gorm:"column:last_synced_at" json:"last_synced_at,omitempty"`
	SyncStatus       *SyncStatus `gorm:"type:varchar(20);column:sync_status" json:"sync_status,omitempty"`
	SyncErrorMessage *string     `gorm:"type:text;column:sync_error_message" json:"sync_error_message,omitempty"`

	// FK to broker_connections table (null for manually managed accounts)
	BrokerConnectionID *uuid.UUID `gorm:"type:uuid;column:broker_connection_id;index" json:"broker_connection_id,omitempty"`

	CreatedAt time.Time      `gorm:"autoCreateTime;column:created_at" json:"created_at"`
	UpdatedAt time.Time      `gorm:"autoUpdateTime;column:updated_at" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index;column:deleted_at" json:"deleted_at,omitempty"`
}

func (Account) TableName() string {
	return "accounts"
}
