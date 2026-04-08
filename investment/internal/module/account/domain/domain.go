package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// Account represents a user's financial account.
type Account struct {
	ID     uuid.UUID `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;column:user_id" json:"userId"`

	AccountName     string      `gorm:"type:varchar(255);not null;column:account_name" json:"accountName"`
	AccountType     AccountType `gorm:"type:varchar(50);not null;column:account_type" json:"accountType"`
	InstitutionName *string     `gorm:"type:varchar(255);column:institution_name" json:"institutionName,omitempty"`

	CurrentBalance   decimal.Decimal  `gorm:"type:decimal(15,2);not null;default:0;column:current_balance" json:"currentBalance"`
	AvailableBalance *decimal.Decimal `gorm:"type:decimal(15,2);column:available_balance" json:"availableBalance,omitempty"`
	Currency         Currency         `gorm:"type:varchar(3);default:'VND';column:currency" json:"currency"`

	AccountNumberMasked    *string          `gorm:"type:varchar(50);column:account_number_masked" json:"accountNumberMasked,omitempty"`
	AccountNumberEncrypted *string          `gorm:"type:text;column:account_number_encrypted" json:"-"`
	CreditLimit            *decimal.Decimal `gorm:"type:decimal(15,2);column:credit_limit" json:"creditLimit,omitempty"`

	IsActive          bool `gorm:"default:true;column:is_active" json:"isActive"`
	IsPrimary         bool `gorm:"default:false;column:is_primary" json:"isPrimary"`
	IncludeInNetWorth bool `gorm:"default:true;column:include_in_net_worth" json:"includeInNetWorth"`

	// IsAutoSync, true for investment - crypto - which has api, false for others
	IsAutoSync       bool        `gorm:"default:false;column:is_auto_sync" json:"isAutoSync"`
	LastSyncedAt     *time.Time  `gorm:"column:last_synced_at" json:"lastSyncedAt,omitempty"`
	SyncStatus       *SyncStatus `gorm:"type:varchar(20);column:sync_status" json:"syncStatus,omitempty"`
	SyncErrorMessage *string     `gorm:"type:text;column:sync_error_message" json:"syncErrorMessage,omitempty"`

	// Broker Integration - nullable FK to broker_connections table
	BrokerConnectionID *uuid.UUID `gorm:"type:uuid;column:broker_connection_id;index" json:"brokerConnectionId,omitempty"`

	CreatedAt time.Time      `gorm:"autoCreateTime;column:created_at" json:"createdAt"`
	UpdatedAt time.Time      `gorm:"autoUpdateTime;column:updated_at" json:"updatedAt"`
	DeletedAt gorm.DeletedAt `gorm:"index;column:deleted_at" json:"deletedAt,omitempty"`
}

// TableName matches the database table.
func (Account) TableName() string {
	return "accounts"
}

func (a *Account) UpdateBalance(amount decimal.Decimal) {
	a.CurrentBalance = a.CurrentBalance.Add(amount)
}
