package command

import (
	"time"

	"mallow/investment/internal/module/portfolio/event"

	"github.com/google/uuid"
)

// RecordTransaction is the command to record any investment transaction.
// AccountID is the aggregate root; UserID is denormalized into the event payload.
type RecordTransaction struct {
	AccountID   uuid.UUID
	UserID      uuid.UUID
	Transaction event.TransactionRecorded
}

// TakeSnapshot is the command to take a portfolio snapshot.
type TakeSnapshot struct {
	AccountID    uuid.UUID
	UserID       uuid.UUID
	SnapshotDate time.Time
	SnapshotType event.SnapshotType
	Payload      event.PortfolioSnapshotTaken
}
