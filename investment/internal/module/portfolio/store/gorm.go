package store

import (
	"context"
	"encoding/json"
	"fmt"

	"mallow/investment/internal/module/portfolio/event"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type gormStore struct {
	db *gorm.DB
}

// NewGORMStore creates a GORM-backed EventStore.
func NewGORMStore(db *gorm.DB) EventStore {
	return &gormStore{db: db}
}

func (s *gormStore) Append(ctx context.Context, aggregateID uuid.UUID, events []event.InvestmentEvent) error {
	if len(events) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&events).Error
}

func (s *gormStore) Load(ctx context.Context, aggregateID uuid.UUID) ([]event.InvestmentEvent, error) {
	var events []event.InvestmentEvent
	err := s.db.WithContext(ctx).
		Where("aggregate_id = ?", aggregateID).
		Order("sequence ASC").
		Find(&events).Error
	return events, err
}

func (s *gormStore) LoadFrom(ctx context.Context, aggregateID uuid.UUID, afterSeq int64) ([]event.InvestmentEvent, error) {
	var events []event.InvestmentEvent
	err := s.db.WithContext(ctx).
		Where("aggregate_id = ? AND sequence > ?", aggregateID, afterSeq).
		Order("sequence ASC").
		Find(&events).Error
	return events, err
}

// MustMarshal marshals v to JSON, panicking on error (only use for known-safe types).
func MustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("event marshal failed: %v", err))
	}
	return b
}
