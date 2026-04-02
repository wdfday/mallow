package store

import (
	"context"

	"mallow/investment/internal/module/portfolio/event"

	"github.com/google/uuid"
)

// EventStore is the interface for appending and replaying events.
type EventStore interface {
	// Append persists new events for an aggregate atomically.
	Append(ctx context.Context, aggregateID uuid.UUID, events []event.InvestmentEvent) error
	// Load returns all events for an aggregate ordered by sequence ASC.
	Load(ctx context.Context, aggregateID uuid.UUID) ([]event.InvestmentEvent, error)
	// LoadFrom returns events for an aggregate with sequence > afterSeq, ordered ASC.
	LoadFrom(ctx context.Context, aggregateID uuid.UUID, afterSeq int64) ([]event.InvestmentEvent, error)
}
