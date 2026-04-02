package command

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/investment/internal/module/portfolio/event"
	"mallow/investment/internal/module/portfolio/projection"
	"mallow/investment/internal/module/portfolio/store"
)

// ---------------------------------------------------------------------------
// In-memory test doubles
// ---------------------------------------------------------------------------

// inMemoryEventStore is a simple, thread-safe in-memory EventStore.
type inMemoryEventStore struct {
	mu     sync.Mutex
	events map[uuid.UUID][]event.InvestmentEvent
	// appendCallCount counts how many times Append was called.
	appendCallCount int
}

func newInMemoryEventStore() *inMemoryEventStore {
	return &inMemoryEventStore{events: make(map[uuid.UUID][]event.InvestmentEvent)}
}

func (s *inMemoryEventStore) Append(_ context.Context, aggregateID uuid.UUID, evts []event.InvestmentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendCallCount++
	s.events[aggregateID] = append(s.events[aggregateID], evts...)
	return nil
}

func (s *inMemoryEventStore) Load(_ context.Context, aggregateID uuid.UUID) ([]event.InvestmentEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	evts := s.events[aggregateID]
	out := make([]event.InvestmentEvent, len(evts))
	copy(out, evts)
	return out, nil
}

func (s *inMemoryEventStore) LoadFrom(_ context.Context, aggregateID uuid.UUID, afterSeq int64) ([]event.InvestmentEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []event.InvestmentEvent
	for _, ev := range s.events[aggregateID] {
		if ev.Sequence > afterSeq {
			out = append(out, ev)
		}
	}
	return out, nil
}

// countByType returns how many stored events of a given type exist for an aggregate.
func (s *inMemoryEventStore) countByType(aggregateID uuid.UUID, et event.EventType) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.events[aggregateID] {
		if ev.EventType == et {
			n++
		}
	}
	return n
}

// allEvents returns a copy of all stored events for an aggregate, sorted by sequence.
func (s *inMemoryEventStore) allEvents(aggregateID uuid.UUID) []event.InvestmentEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	evts := make([]event.InvestmentEvent, len(s.events[aggregateID]))
	copy(evts, s.events[aggregateID])
	sort.Slice(evts, func(i, j int) bool { return evts[i].Sequence < evts[j].Sequence })
	return evts
}

// ---------------------------------------------------------------------------

// inMemorySnapshotStore is a simple in-memory SnapshotStore.
type inMemorySnapshotStore struct {
	mu        sync.Mutex
	snapshots map[uuid.UUID][]store.AggregateSnapshot
	saveCount int
}

func newInMemorySnapshotStore() *inMemorySnapshotStore {
	return &inMemorySnapshotStore{snapshots: make(map[uuid.UUID][]store.AggregateSnapshot)}
}

func (s *inMemorySnapshotStore) Save(_ context.Context, snap store.AggregateSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCount++
	s.snapshots[snap.AggregateID] = append(s.snapshots[snap.AggregateID], snap)
	return nil
}

func (s *inMemorySnapshotStore) LoadLatest(_ context.Context, aggregateID uuid.UUID) (*store.AggregateSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snaps := s.snapshots[aggregateID]
	if len(snaps) == 0 {
		return nil, nil
	}
	latest := snaps[0]
	for _, sn := range snaps[1:] {
		if sn.Sequence > latest.Sequence {
			latest = sn
		}
	}
	cp := latest
	return &cp, nil
}

// ---------------------------------------------------------------------------

// capturePublisher records every published event and can be set to fail.
type capturePublisher struct {
	mu        sync.Mutex
	published []event.InvestmentEvent
	failWith  error
}

func (p *capturePublisher) Publish(_ context.Context, ev event.InvestmentEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failWith != nil {
		return p.failWith
	}
	p.published = append(p.published, ev)
	return nil
}

func (p *capturePublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.published)
}

// ---------------------------------------------------------------------------

// recordingProjector implements projection.Projector, records events, and can be set to fail.
type recordingProjector struct {
	mu        sync.Mutex
	projected []event.InvestmentEvent
	failWith  error
}

func (r *recordingProjector) Project(_ context.Context, ev event.InvestmentEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return r.failWith
	}
	r.projected = append(r.projected, ev)
	return nil
}

func (r *recordingProjector) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.projected)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newHandler(
	es *inMemoryEventStore,
	ss *inMemorySnapshotStore,
	proj *recordingProjector,
	pub *capturePublisher,
) *Handler {
	runner := projection.NewRunner(proj)
	return NewHandler(es, ss, runner, pub, discardLogger())
}

func makeTx(txType event.TransactionType) event.TransactionRecorded {
	return event.TransactionRecorded{
		Type:            txType,
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        10,
		PricePerUnit:    150.0,
		Amount:          1500.0,
		TransactionDate: time.Now().UTC(),
	}
}

// seedEventsForAccount directly inserts raw events into the event store,
// simulating a pre-existing aggregate history. seq starts from startSeq+1.
func seedEventsForAccount(
	es *inMemoryEventStore,
	accountID uuid.UUID,
	startSeq int64,
	count int,
	evtType event.EventType,
) {
	for i := range count {
		seq := startSeq + int64(i) + 1
		payload, _ := json.Marshal(map[string]any{"seq": seq})
		es.events[accountID] = append(es.events[accountID], event.InvestmentEvent{
			ID:            uuid.New(),
			AggregateID:   accountID,
			AggregateType: "Account",
			EventType:     evtType,
			EventVersion:  1,
			Sequence:      seq,
			Payload:       payload,
			OccurredAt:    time.Now().UTC(),
		})
	}
}

// seedSnapshotForAccount inserts a snapshot at the given sequence into ss.
func seedSnapshotForAccount(
	ss *inMemorySnapshotStore,
	accountID, userID uuid.UUID,
	seq int64,
	initialized bool,
) {
	state := store.AggregateState{
		AccountID:   accountID,
		UserID:      userID,
		Initialized: initialized,
		LastSeq:     seq,
	}
	stateJSON, _ := json.Marshal(state)
	ss.snapshots[accountID] = append(ss.snapshots[accountID], store.AggregateSnapshot{
		ID:            uuid.New(),
		AggregateID:   accountID,
		AggregateType: "Account",
		Sequence:      seq,
		State:         stateJSON,
	})
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Test 1: HandleRecordTransaction - new account
// First transaction on a brand-new account must emit AccountInitialized + TransactionRecorded.
func TestHandleRecordTransaction_NewAccount(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{}
	h := newHandler(es, ss, proj, pub)

	accountID := uuid.New()
	userID := uuid.New()
	cmd := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: makeTx(event.TransactionTypeBuy),
	}

	err := h.HandleRecordTransaction(context.Background(), cmd)
	require.NoError(t, err)

	evts := es.allEvents(accountID)
	require.Len(t, evts, 2, "expect AccountInitialized + TransactionRecorded")
	assert.Equal(t, event.EventTypeAccountInitialized, evts[0].EventType)
	assert.Equal(t, int64(1), evts[0].Sequence)
	assert.Equal(t, event.EventTypeTransactionRecorded, evts[1].EventType)
	assert.Equal(t, int64(2), evts[1].Sequence)

	// Projector and publisher should receive both events
	assert.Equal(t, 2, proj.count(), "projector should receive both events")
	assert.Equal(t, 2, pub.count(), "publisher should receive both events")
}

// Test 2: HandleRecordTransaction - existing account
// Second transaction on an already-initialized account must emit only TransactionRecorded.
func TestHandleRecordTransaction_ExistingAccount(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{}
	h := newHandler(es, ss, proj, pub)

	accountID := uuid.New()
	userID := uuid.New()
	cmd := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: makeTx(event.TransactionTypeBuy),
	}

	// First call initializes the account
	require.NoError(t, h.HandleRecordTransaction(context.Background(), cmd))

	// Reset counters by creating new projector and publisher, reuse stores
	proj2 := &recordingProjector{}
	pub2 := &capturePublisher{}
	runner2 := projection.NewRunner(proj2)
	h2 := NewHandler(es, ss, runner2, pub2, discardLogger())

	// Second call: account is already initialized
	cmd2 := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: makeTx(event.TransactionTypeSell),
	}
	require.NoError(t, h2.HandleRecordTransaction(context.Background(), cmd2))

	// Only 1 new event (TransactionRecorded), no extra AccountInitialized
	assert.Equal(t, 1, es.countByType(accountID, event.EventTypeAccountInitialized),
		"AccountInitialized should exist exactly once")
	assert.Equal(t, 2, es.countByType(accountID, event.EventTypeTransactionRecorded),
		"two TransactionRecorded events total")

	evts := es.allEvents(accountID)
	require.Len(t, evts, 3)
	// Sequences must be strictly increasing
	assert.Equal(t, int64(1), evts[0].Sequence)
	assert.Equal(t, int64(2), evts[1].Sequence)
	assert.Equal(t, int64(3), evts[2].Sequence)

	// Second handler only saw 1 event
	assert.Equal(t, 1, proj2.count())
	assert.Equal(t, 1, pub2.count())
}

// Test 3: HandleRecordTransactionBatch - multiple transactions
// 3 transactions in one batch → single Append call, 4 events total (AccountInitialized + 3x TransactionRecorded).
func TestHandleRecordTransactionBatch_MultipleTransactions(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{}
	h := newHandler(es, ss, proj, pub)

	accountID := uuid.New()
	userID := uuid.New()

	txns := []event.TransactionRecorded{
		makeTx(event.TransactionTypeBuy),
		makeTx(event.TransactionTypeBuy),
		makeTx(event.TransactionTypeSell),
	}

	err := h.HandleRecordTransactionBatch(context.Background(), accountID, userID, txns)
	require.NoError(t, err)

	// Single Append call for the entire batch
	assert.Equal(t, 1, es.appendCallCount, "batch should use a single Append call")

	evts := es.allEvents(accountID)
	require.Len(t, evts, 4, "AccountInitialized + 3 TransactionRecorded")
	assert.Equal(t, event.EventTypeAccountInitialized, evts[0].EventType)
	for i := 1; i <= 3; i++ {
		assert.Equal(t, event.EventTypeTransactionRecorded, evts[i].EventType,
			"event %d should be TransactionRecorded", i)
	}

	// All 4 events projected and published
	assert.Equal(t, 4, proj.count())
	assert.Equal(t, 4, pub.count())
}

// Test 4: HandleTakeSnapshot - uninitialized account
// No prior events → returns nil without committing anything.
func TestHandleTakeSnapshot_UninitializedAccount(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{}
	h := newHandler(es, ss, proj, pub)

	accountID := uuid.New()
	userID := uuid.New()
	cmd := TakeSnapshot{
		AccountID:    accountID,
		UserID:       userID,
		SnapshotDate: time.Now().UTC(),
		SnapshotType: event.SnapshotTypeDaily,
		Payload:      event.PortfolioSnapshotTaken{TotalValue: 1000},
	}

	err := h.HandleTakeSnapshot(context.Background(), cmd)
	require.NoError(t, err)

	evts := es.allEvents(accountID)
	assert.Empty(t, evts, "no events should be committed for uninitialized account")
	assert.Equal(t, 0, proj.count())
	assert.Equal(t, 0, pub.count())
}

// Test 5: HandleTakeSnapshot - initialized account
// Account with prior transactions → PortfolioSnapshotTaken event committed with correct UserID.
func TestHandleTakeSnapshot_InitializedAccount(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{}
	h := newHandler(es, ss, proj, pub)

	accountID := uuid.New()
	userID := uuid.New()

	// Initialize the account first
	txCmd := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: makeTx(event.TransactionTypeDeposit),
	}
	require.NoError(t, h.HandleRecordTransaction(context.Background(), txCmd))

	snapshotDate := time.Now().UTC()
	snapCmd := TakeSnapshot{
		AccountID:    accountID,
		UserID:       userID,
		SnapshotDate: snapshotDate,
		SnapshotType: event.SnapshotTypeDaily,
		Payload: event.PortfolioSnapshotTaken{
			TotalValue:  5000.0,
			CashBalance: 5000.0,
		},
	}
	err := h.HandleTakeSnapshot(context.Background(), snapCmd)
	require.NoError(t, err)

	evts := es.allEvents(accountID)
	// seq 1: AccountInitialized, seq 2: TransactionRecorded, seq 3: PortfolioSnapshotTaken
	require.Len(t, evts, 3)
	snapEvt := evts[2]
	assert.Equal(t, event.EventTypePortfolioSnapshotTaken, snapEvt.EventType)

	var payload event.PortfolioSnapshotTaken
	require.NoError(t, json.Unmarshal(snapEvt.Payload, &payload))
	assert.Equal(t, userID, payload.UserID, "UserID must be injected from cmd.UserID")
	assert.Equal(t, event.SnapshotTypeDaily, payload.SnapshotType)
	assert.InDelta(t, 5000.0, payload.TotalValue, 0.001)
}

// Test 6: loadAccount - uses snapshot + partial replay
// Snapshot at seq=5, then events at seq=6 and seq=7. After one more transaction, LastSeq=8.
func TestLoadAccount_UsesSnapshotAndPartialReplay(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{}

	accountID := uuid.New()
	userID := uuid.New()

	// Pre-seed snapshot at seq=5 (account is already initialized)
	seedSnapshotForAccount(ss, accountID, userID, 5, true)

	// Pre-seed two events in the event store at seq=6 and seq=7 (after snapshot)
	seedEventsForAccount(es, accountID, 5, 2, event.EventTypeTransactionRecorded)

	h := newHandler(es, ss, proj, pub)
	cmd := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: makeTx(event.TransactionTypeBuy),
	}
	err := h.HandleRecordTransaction(context.Background(), cmd)
	require.NoError(t, err)

	// The newly committed event should be seq=8 (5 from snapshot + 2 replayed + 1 new)
	evts := es.allEvents(accountID)
	// The seeded events at seq 6 and 7 plus the newly committed event at seq 8
	require.Len(t, evts, 3, "2 pre-seeded + 1 new event in the store")
	newEvt := evts[2]
	assert.Equal(t, event.EventTypeTransactionRecorded, newEvt.EventType)
	assert.Equal(t, int64(8), newEvt.Sequence, "new event must be seq=8 after snapshot(5)+replay(6,7)")
}

// Test 7: commitAndProject - projector failure is non-fatal
// When projector returns an error, HandleRecordTransaction still returns nil
// and events are still committed to the store.
func TestCommitAndProject_ProjectorFailureIsNonFatal(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{failWith: errors.New("projector boom")}
	pub := &capturePublisher{}
	h := newHandler(es, ss, proj, pub)

	accountID := uuid.New()
	userID := uuid.New()
	cmd := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: makeTx(event.TransactionTypeBuy),
	}

	err := h.HandleRecordTransaction(context.Background(), cmd)
	assert.NoError(t, err, "projector failure must not propagate as an error")

	// Events must still be committed
	evts := es.allEvents(accountID)
	assert.Len(t, evts, 2, "events must be committed despite projector error")
}

// Test 8: commitAndProject - publisher failure is non-fatal
// When publisher returns an error, HandleRecordTransaction still returns nil
// and events are still committed to the store.
func TestCommitAndProject_PublisherFailureIsNonFatal(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{failWith: errors.New("nats publish boom")}
	h := newHandler(es, ss, proj, pub)

	accountID := uuid.New()
	userID := uuid.New()
	cmd := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: makeTx(event.TransactionTypeBuy),
	}

	err := h.HandleRecordTransaction(context.Background(), cmd)
	assert.NoError(t, err, "publisher failure must not propagate as an error")

	// Events must still be committed to the store
	evts := es.allEvents(accountID)
	assert.Len(t, evts, 2, "events must be committed despite publisher error")
}

// Test 9: auto-snapshot at seq % 50 == 0
// Pre-populate aggregate to LastSeq=49, then one more transaction → LastSeq=50 →
// snapshotStore.Save must be called with Sequence=50.
func TestAutoSnapshot_AtInterval(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{}

	accountID := uuid.New()
	userID := uuid.New()

	// Seed snapshot at seq=49, initialized=true so account doesn't re-initialize
	seedSnapshotForAccount(ss, accountID, userID, 49, true)
	// No events after the snapshot (seq=49 is latest)

	h := newHandler(es, ss, proj, pub)
	cmd := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: makeTx(event.TransactionTypeBuy),
	}
	err := h.HandleRecordTransaction(context.Background(), cmd)
	require.NoError(t, err)

	// The new event lands at seq=50 (snapshot was at 49, no additional events to replay)
	evts := es.allEvents(accountID)
	require.Len(t, evts, 1)
	assert.Equal(t, int64(50), evts[0].Sequence)

	// Auto-snapshot must have been saved
	require.Equal(t, 1, ss.saveCount, "snapshotStore.Save should be called once at seq=50")

	latestSnap, err := ss.LoadLatest(context.Background(), accountID)
	require.NoError(t, err)
	require.NotNil(t, latestSnap)
	assert.Equal(t, int64(50), latestSnap.Sequence, "snapshot must be at seq=50")

	var state store.AggregateState
	require.NoError(t, json.Unmarshal(latestSnap.State, &state))
	assert.Equal(t, int64(50), state.LastSeq)
	assert.True(t, state.Initialized)
	assert.Equal(t, accountID, state.AccountID)
	assert.Equal(t, userID, state.UserID)
}

// Test 10: HandleRecordTransaction - UserID injected into payload
// cmd.UserID must be set in the TransactionRecorded event payload.
func TestHandleRecordTransaction_UserIDInjectedIntoPayload(t *testing.T) {
	es := newInMemoryEventStore()
	ss := newInMemorySnapshotStore()
	proj := &recordingProjector{}
	pub := &capturePublisher{}
	h := newHandler(es, ss, proj, pub)

	accountID := uuid.New()
	userID := uuid.New()
	tx := makeTx(event.TransactionTypeBuy)
	// Intentionally leave UserID zero — handler must override it
	tx.UserID = uuid.Nil

	cmd := RecordTransaction{
		AccountID:   accountID,
		UserID:      userID,
		Transaction: tx,
	}

	err := h.HandleRecordTransaction(context.Background(), cmd)
	require.NoError(t, err)

	evts := es.allEvents(accountID)
	require.Len(t, evts, 2)

	// Find the TransactionRecorded event
	var txEvt event.InvestmentEvent
	for _, ev := range evts {
		if ev.EventType == event.EventTypeTransactionRecorded {
			txEvt = ev
			break
		}
	}
	require.Equal(t, event.EventTypeTransactionRecorded, txEvt.EventType)

	var payload event.TransactionRecorded
	require.NoError(t, json.Unmarshal(txEvt.Payload, &payload))
	assert.Equal(t, userID, payload.UserID,
		"cmd.UserID must be injected into the TransactionRecorded payload")
}
