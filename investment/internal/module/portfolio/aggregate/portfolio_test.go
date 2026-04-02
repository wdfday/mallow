package aggregate_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/investment/internal/module/portfolio/aggregate"
	"mallow/investment/internal/module/portfolio/event"
)

func newIDs() (accountID, userID uuid.UUID) {
	return uuid.New(), uuid.New()
}

func makeTx(userID uuid.UUID) event.TransactionRecorded {
	return event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeBuy,
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        10,
		PricePerUnit:    150.00,
		Amount:          1500.00,
		TransactionDate: time.Now().UTC(),
	}
}

// 1. New() creates an empty aggregate with correct IDs.
func TestNew_CreatesEmptyAggregate(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	assert.Equal(t, accountID, a.AccountID)
	assert.Equal(t, userID, a.UserID)
	assert.False(t, a.Initialized)
	assert.Equal(t, int64(0), a.LastSeq)
	assert.Empty(t, a.UncommittedEvents())
}

// 2. RecordTransaction on a new account raises AccountInitialized + TransactionRecorded (2 events, seq 1 and 2).
func TestRecordTransaction_OnNewAccount_RaisesTwoEvents(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	err := a.RecordTransaction(makeTx(userID))
	require.NoError(t, err)

	evts := a.UncommittedEvents()
	require.Len(t, evts, 2)

	assert.Equal(t, event.EventTypeAccountInitialized, evts[0].EventType)
	assert.Equal(t, int64(1), evts[0].Sequence)

	assert.Equal(t, event.EventTypeTransactionRecorded, evts[1].EventType)
	assert.Equal(t, int64(2), evts[1].Sequence)
}

// 3. RecordTransaction on an already-initialized account raises only TransactionRecorded (1 event).
func TestRecordTransaction_OnInitializedAccount_RaisesOneEvent(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	// First call initializes
	require.NoError(t, a.RecordTransaction(makeTx(userID)))
	a.ClearUncommitted()

	// Second call should produce only TransactionRecorded
	require.NoError(t, a.RecordTransaction(makeTx(userID)))

	evts := a.UncommittedEvents()
	require.Len(t, evts, 1)
	assert.Equal(t, event.EventTypeTransactionRecorded, evts[0].EventType)
}

// 4. Multiple RecordTransaction calls increment sequence correctly.
func TestRecordTransaction_MultipleCallsIncrementSequence(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	// seq 1 = AccountInitialized, seq 2 = TransactionRecorded
	require.NoError(t, a.RecordTransaction(makeTx(userID)))
	// seq 3 = TransactionRecorded
	require.NoError(t, a.RecordTransaction(makeTx(userID)))
	// seq 4 = TransactionRecorded
	require.NoError(t, a.RecordTransaction(makeTx(userID)))

	evts := a.UncommittedEvents()
	require.Len(t, evts, 4)
	for i, ev := range evts {
		assert.Equal(t, int64(i+1), ev.Sequence, "event index %d should have sequence %d", i, i+1)
	}
	assert.Equal(t, int64(4), a.LastSeq)
}

// 5. Replay() reconstructs LastSeq and Initialized state from events.
func TestReplay_ReconstructsState(t *testing.T) {
	accountID, userID := newIDs()

	// Build a representative event history
	history := []event.InvestmentEvent{
		{
			AggregateID: accountID,
			EventType:   event.EventTypeAccountInitialized,
			Sequence:    1,
			OccurredAt:  time.Now().UTC(),
			Payload:     json.RawMessage(`{}`),
		},
		{
			AggregateID: accountID,
			EventType:   event.EventTypeTransactionRecorded,
			Sequence:    2,
			OccurredAt:  time.Now().UTC(),
			Payload:     json.RawMessage(`{}`),
		},
		{
			AggregateID: accountID,
			EventType:   event.EventTypeTransactionRecorded,
			Sequence:    3,
			OccurredAt:  time.Now().UTC(),
			Payload:     json.RawMessage(`{}`),
		},
	}

	a := aggregate.New(accountID, userID)
	a.Replay(history)

	assert.True(t, a.Initialized)
	assert.Equal(t, int64(3), a.LastSeq)
}

// 6. RestoreFromSnapshot() creates aggregate with given state, then Replay() only applies events after.
func TestRestoreFromSnapshot_ThenReplayRemainingEvents(t *testing.T) {
	accountID, userID := newIDs()

	// Snapshot at seq=5 (already initialized)
	a := aggregate.RestoreFromSnapshot(accountID, userID, true, 5)
	assert.True(t, a.Initialized)
	assert.Equal(t, int64(5), a.LastSeq)

	// Replay events that came after the snapshot
	laterEvents := []event.InvestmentEvent{
		{
			AggregateID: accountID,
			EventType:   event.EventTypeTransactionRecorded,
			Sequence:    6,
			OccurredAt:  time.Now().UTC(),
			Payload:     json.RawMessage(`{}`),
		},
		{
			AggregateID: accountID,
			EventType:   event.EventTypeTransactionRecorded,
			Sequence:    7,
			OccurredAt:  time.Now().UTC(),
			Payload:     json.RawMessage(`{}`),
		},
	}
	a.Replay(laterEvents)

	assert.True(t, a.Initialized)
	assert.Equal(t, int64(7), a.LastSeq)
}

// 7. ClearUncommitted() empties the uncommitted buffer.
func TestClearUncommitted_EmptiesBuffer(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	require.NoError(t, a.RecordTransaction(makeTx(userID)))
	require.NotEmpty(t, a.UncommittedEvents())

	a.ClearUncommitted()

	assert.Empty(t, a.UncommittedEvents())
}

// 8. UncommittedEvents() returns staged events.
func TestUncommittedEvents_ReturnsStagedEvents(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	// Before any operation, buffer is empty
	assert.Empty(t, a.UncommittedEvents())

	require.NoError(t, a.RecordTransaction(makeTx(userID)))

	staged := a.UncommittedEvents()
	assert.Len(t, staged, 2) // AccountInitialized + TransactionRecorded

	// Staged slice should contain both expected event types
	types := make([]event.EventType, len(staged))
	for i, ev := range staged {
		types[i] = ev.EventType
	}
	assert.Contains(t, types, event.EventTypeAccountInitialized)
	assert.Contains(t, types, event.EventTypeTransactionRecorded)
}

// 9. TakeSnapshot() raises PortfolioSnapshotTaken event.
func TestTakeSnapshot_RaisesSnapshotEvent(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	snap := event.PortfolioSnapshotTaken{
		UserID:       userID,
		SnapshotDate: time.Now().UTC(),
		SnapshotType: event.SnapshotTypeDaily,
		TotalValue:   10000.00,
	}

	err := a.TakeSnapshot(snap)
	require.NoError(t, err)

	evts := a.UncommittedEvents()
	require.Len(t, evts, 1)
	assert.Equal(t, event.EventTypePortfolioSnapshotTaken, evts[0].EventType)
	assert.Equal(t, int64(1), evts[0].Sequence)
}

// 10. TakeSnapshot() does NOT call EnsureInitialized (no AccountInitialized event raised).
func TestTakeSnapshot_DoesNotEnsureInitialized(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	// Account is NOT initialized before the snapshot
	assert.False(t, a.Initialized)

	snap := event.PortfolioSnapshotTaken{
		UserID:       userID,
		SnapshotDate: time.Now().UTC(),
		SnapshotType: event.SnapshotTypeManual,
	}

	err := a.TakeSnapshot(snap)
	require.NoError(t, err)

	evts := a.UncommittedEvents()
	require.Len(t, evts, 1)
	// Must be snapshot, not AccountInitialized
	assert.Equal(t, event.EventTypePortfolioSnapshotTaken, evts[0].EventType)
	// Initialized state must remain false (TakeSnapshot never sets it)
	assert.False(t, a.Initialized)
}

// 11. Event payload is valid JSON and unmarshallable into the expected struct.
func TestEvent_PayloadIsValidJSON(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	tx := makeTx(userID)
	require.NoError(t, a.RecordTransaction(tx))

	evts := a.UncommittedEvents()
	require.Len(t, evts, 2)

	// Verify AccountInitialized payload
	var initPayload event.AccountInitialized
	require.NoError(t, json.Unmarshal(evts[0].Payload, &initPayload))
	assert.Equal(t, userID, initPayload.UserID)

	// Verify TransactionRecorded payload
	var txPayload event.TransactionRecorded
	require.NoError(t, json.Unmarshal(evts[1].Payload, &txPayload))
	assert.Equal(t, tx.Symbol, txPayload.Symbol)
	assert.Equal(t, tx.Amount, txPayload.Amount)
	assert.Equal(t, tx.Type, txPayload.Type)
}

// 12. Event AggregateID matches account ID.
func TestEvent_AggregateIDMatchesAccountID(t *testing.T) {
	accountID, userID := newIDs()
	a := aggregate.New(accountID, userID)

	require.NoError(t, a.RecordTransaction(makeTx(userID)))

	for _, ev := range a.UncommittedEvents() {
		assert.Equal(t, accountID, ev.AggregateID,
			"event %s: AggregateID should equal accountID", ev.EventType)
	}
}
