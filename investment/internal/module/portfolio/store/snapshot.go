package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AggregateSnapshot stores a serialized aggregate state at a given sequence,
// allowing loadAccount to skip replaying the full event history.
type AggregateSnapshot struct {
	ID            uuid.UUID       `gorm:"type:uuid;default:uuidv7();primaryKey"`
	AggregateID   uuid.UUID       `gorm:"type:uuid;not null;index;column:aggregate_id"`
	AggregateType string          `gorm:"type:varchar(50);not null;default:'Account';column:aggregate_type"`
	Sequence      int64           `gorm:"not null;column:sequence"`
	State         json.RawMessage `gorm:"type:jsonb;not null;column:state"`
	CreatedAt     time.Time       `gorm:"not null;default:now();column:created_at"`
}

func (AggregateSnapshot) TableName() string {
	return "aggregate_snapshots"
}

// AggregateState is the serializable representation of Account aggregate state.
type AggregateState struct {
	AccountID   uuid.UUID `json:"account_id"`
	UserID      uuid.UUID `json:"user_id"`
	Initialized bool      `json:"initialized"`
	LastSeq     int64     `json:"last_seq"`
}

// SnapshotStore manages aggregate-level snapshots.
type SnapshotStore interface {
	// Save persists an aggregate snapshot.
	Save(ctx context.Context, snap AggregateSnapshot) error
	// LoadLatest returns the most recent snapshot for the aggregate, or nil if none.
	LoadLatest(ctx context.Context, aggregateID uuid.UUID) (*AggregateSnapshot, error)
}

type gormSnapshotStore struct {
	db *gorm.DB
}

func NewGORMSnapshotStore(db *gorm.DB) SnapshotStore {
	return &gormSnapshotStore{db: db}
}

func (s *gormSnapshotStore) Save(ctx context.Context, snap AggregateSnapshot) error {
	return s.db.WithContext(ctx).Create(&snap).Error
}

func (s *gormSnapshotStore) LoadLatest(ctx context.Context, aggregateID uuid.UUID) (*AggregateSnapshot, error) {
	var snap AggregateSnapshot
	err := s.db.WithContext(ctx).
		Where("aggregate_id = ?", aggregateID).
		Order("sequence DESC").
		First(&snap).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &snap, nil
}
