package aggregate

import (
	"encoding/json"
	"fmt"
	"time"

	"mallow/investment/internal/module/portfolio/event"

	"github.com/google/uuid"
)

// Account is the aggregate root for a single investment account.
// Each account (e.g. Binance, SSI, manual-cash) has its own event stream
// with independent sequence numbering and concurrency control.
type Account struct {
	AccountID   uuid.UUID
	UserID      uuid.UUID // denormalized — carried in every event payload for read-model queries
	Initialized bool
	LastSeq     int64
	UpdatedAt   time.Time

	uncommitted []event.InvestmentEvent
}

// New creates an empty Account aggregate.
func New(accountID, userID uuid.UUID) *Account {
	return &Account{AccountID: accountID, UserID: userID}
}

// RestoreFromSnapshot reconstitutes an aggregate from a persisted snapshot.
func RestoreFromSnapshot(accountID, userID uuid.UUID, initialized bool, lastSeq int64) *Account {
	return &Account{
		AccountID:   accountID,
		UserID:      userID,
		Initialized: initialized,
		LastSeq:     lastSeq,
	}
}

// Raise stages a new event with the next sequence number.
func (a *Account) Raise(evtType event.EventType, payload any, metadata any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	var metaJSON json.RawMessage
	if metadata != nil {
		metaJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
	}

	a.LastSeq++
	ev := event.InvestmentEvent{
		AggregateID:   a.AccountID,
		AggregateType: "Account",
		EventType:     evtType,
		EventVersion:  1,
		Sequence:      a.LastSeq,
		Payload:       payloadJSON,
		Metadata:      metaJSON,
		OccurredAt:    time.Now().UTC(),
	}

	a.apply(ev)
	a.uncommitted = append(a.uncommitted, ev)
	return nil
}

// apply updates in-memory state from a persisted or staged event.
func (a *Account) apply(ev event.InvestmentEvent) {
	a.LastSeq = ev.Sequence
	a.UpdatedAt = ev.OccurredAt
	if ev.EventType == event.EventTypeAccountInitialized {
		a.Initialized = true
	}
}

// Replay reconstructs the aggregate from a history of persisted events.
func (a *Account) Replay(events []event.InvestmentEvent) {
	for _, ev := range events {
		a.apply(ev)
	}
}

// UncommittedEvents returns staged events not yet written to the store.
func (a *Account) UncommittedEvents() []event.InvestmentEvent {
	return a.uncommitted
}

// ClearUncommitted is called after a successful Append.
func (a *Account) ClearUncommitted() {
	a.uncommitted = nil
}

// EnsureInitialized stages an AccountInitialized event if this is the first operation.
func (a *Account) EnsureInitialized() error {
	if !a.Initialized {
		return a.Raise(event.EventTypeAccountInitialized, event.AccountInitialized{
			UserID:        a.UserID,
			InitializedAt: time.Now().UTC(),
		}, nil)
	}
	return nil
}

// RecordTransaction stages a TransactionRecorded event.
func (a *Account) RecordTransaction(tx event.TransactionRecorded) error {
	if err := a.EnsureInitialized(); err != nil {
		return err
	}
	return a.Raise(event.EventTypeTransactionRecorded, tx, nil)
}

// TakeSnapshot stages a PortfolioSnapshotTaken event.
func (a *Account) TakeSnapshot(ev event.PortfolioSnapshotTaken) error {
	return a.Raise(event.EventTypePortfolioSnapshotTaken, ev, nil)
}
