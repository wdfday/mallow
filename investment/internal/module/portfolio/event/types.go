package event

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventType identifies the kind of domain event.
type EventType string

const (
	EventTypeAccountInitialized     EventType = "AccountInitialized"
	EventTypeTransactionRecorded    EventType = "TransactionRecorded"
	EventTypePortfolioSnapshotTaken EventType = "PortfolioSnapshotTaken"
)

// TransactionType is the sub-type within TransactionRecorded.
type TransactionType string

const (
	TransactionTypeBuy             TransactionType = "buy"
	TransactionTypeSell            TransactionType = "sell"
	TransactionTypeDividend        TransactionType = "dividend"
	TransactionTypeDeposit         TransactionType = "deposit"
	TransactionTypeWithdrawal      TransactionType = "withdrawal"
	TransactionTypeFee             TransactionType = "fee"
	TransactionTypeDerivativeOpen  TransactionType = "derivative_open"
	TransactionTypeDerivativeClose TransactionType = "derivative_close"
)

// SnapshotType identifies the period of a portfolio snapshot.
type SnapshotType string

const (
	SnapshotTypeDaily   SnapshotType = "daily"
	SnapshotTypeWeekly  SnapshotType = "weekly"
	SnapshotTypeMonthly SnapshotType = "monthly"
	SnapshotTypeManual  SnapshotType = "manual"
)

// InvestmentEvent is the database row for the event store.
// AggregateID is account_id (the aggregate root).
type InvestmentEvent struct {
	ID            uuid.UUID       `gorm:"type:uuid;default:uuidv7();primaryKey" json:"id"`
	AggregateID   uuid.UUID       `gorm:"type:uuid;not null;index;column:aggregate_id" json:"aggregate_id"`
	AggregateType string          `gorm:"type:varchar(50);not null;default:'Account';column:aggregate_type" json:"aggregate_type"`
	EventType     EventType       `gorm:"type:varchar(100);not null;index;column:event_type" json:"event_type"`
	EventVersion  int             `gorm:"not null;default:1;column:event_version" json:"event_version"`
	Sequence      int64           `gorm:"not null;column:sequence" json:"sequence"`
	Payload       json.RawMessage `gorm:"type:jsonb;not null;column:payload" json:"payload"`
	Metadata      json.RawMessage `gorm:"type:jsonb;column:metadata" json:"metadata"`
	OccurredAt    time.Time       `gorm:"not null;default:now();column:occurred_at" json:"occurred_at"`
}

func (InvestmentEvent) TableName() string {
	return "investment_events"
}
