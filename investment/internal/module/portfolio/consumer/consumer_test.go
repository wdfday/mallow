package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/investment/internal/module/portfolio/command"
	"mallow/investment/internal/module/portfolio/event"
	"mallow/pkg/contracts"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeMsg(t *testing.T, payload any) *nats.Msg {
	t.Helper()
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	return &nats.Msg{Data: data}
}

func validMsg(accountID, userID uuid.UUID) contracts.InvestmentTransactionMsg {
	return contracts.InvestmentTransactionMsg{
		AccountID:       accountID.String(),
		UserID:          userID.String(),
		Type:            "buy",
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        10.0,
		PricePerUnit:    150.0,
		Amount:          1500.0,
		Fees:            1.5,
		ExternalID:      "ord-001",
		TradeID:         "fill-abc123",
		Kind:            "fill",
		Source:          "bot",
		BotID:           "bot-xyz",
		TransactionDate: time.Now().UTC(),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestConsumer(h transactionHandler) *Consumer {
	return &Consumer{
		sub:     nil,
		handler: h,
		logger:  discardLogger(),
	}
}

// ---------------------------------------------------------------------------
// stubHandler
// ---------------------------------------------------------------------------

type stubHandler struct {
	mu          sync.Mutex
	singleCalls []command.RecordTransaction
	batchCalls  []batchCall
	failWith    error
}

type batchCall struct {
	accountID uuid.UUID
	userID    uuid.UUID
	txns      []event.TransactionRecorded
}

func (s *stubHandler) HandleRecordTransaction(_ context.Context, cmd command.RecordTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWith != nil {
		return s.failWith
	}
	s.singleCalls = append(s.singleCalls, cmd)
	return nil
}

func (s *stubHandler) HandleRecordTransactionBatch(_ context.Context, accountID, userID uuid.UUID, txns []event.TransactionRecorded) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWith != nil {
		return s.failWith
	}
	s.batchCalls = append(s.batchCalls, batchCall{accountID, userID, txns})
	return nil
}

func (s *stubHandler) singleCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.singleCalls)
}

func (s *stubHandler) batchCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.batchCalls)
}

// ---------------------------------------------------------------------------
// parse() tests
// ---------------------------------------------------------------------------

func TestParse_ValidBuyMessage(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()
	tm := validMsg(accountID, userID)
	c := newTestConsumer(&stubHandler{})

	msg := makeMsg(t, tm)
	ab, err := c.parse(msg)
	require.NoError(t, err)

	assert.Equal(t, accountID, ab.accountID)
	assert.Equal(t, userID, ab.userID)
	require.Len(t, ab.txns, 1)

	tx := ab.txns[0]
	assert.Equal(t, event.TransactionType("buy"), tx.Type)
	assert.Equal(t, "AAPL", tx.Symbol)
	assert.Equal(t, "USD", tx.Currency)
	assert.Equal(t, "fill", tx.Kind)
	assert.Equal(t, "ord-001", tx.ExternalID)
	assert.Equal(t, "bot-xyz", tx.BotID)
}

func TestParse_SourceAlwaysSync(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()
	tm := validMsg(accountID, userID)
	tm.Source = "manual" // consumer must override this
	c := newTestConsumer(&stubHandler{})

	ab, err := c.parse(makeMsg(t, tm))
	require.NoError(t, err)
	assert.Equal(t, "sync", ab.txns[0].Source)
}

func TestParse_KindForwarded(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()
	tm := validMsg(accountID, userID)
	tm.Kind = "fill"
	c := newTestConsumer(&stubHandler{})

	ab, err := c.parse(makeMsg(t, tm))
	require.NoError(t, err)
	assert.Equal(t, "fill", ab.txns[0].Kind)
}

func TestParse_EmptyKind(t *testing.T) {
	accountID := uuid.New()
	userID := uuid.New()
	tm := validMsg(accountID, userID)
	tm.Kind = ""
	c := newTestConsumer(&stubHandler{})

	ab, err := c.parse(makeMsg(t, tm))
	require.NoError(t, err)
	assert.Equal(t, "", ab.txns[0].Kind)
}

func TestParse_InvalidJSON(t *testing.T) {
	c := newTestConsumer(&stubHandler{})
	msg := &nats.Msg{Data: []byte("not-json{")}
	_, err := c.parse(msg)
	assert.Error(t, err)
}

func TestParse_InvalidAccountUUID(t *testing.T) {
	userID := uuid.New()
	tm := validMsg(uuid.New(), userID)
	tm.AccountID = "not-a-uuid"
	c := newTestConsumer(&stubHandler{})

	_, err := c.parse(makeMsg(t, tm))
	assert.Error(t, err)
}

func TestParse_InvalidUserUUID(t *testing.T) {
	accountID := uuid.New()
	tm := validMsg(accountID, uuid.New())
	tm.UserID = "bad-uuid"
	c := newTestConsumer(&stubHandler{})

	_, err := c.parse(makeMsg(t, tm))
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// buffer tests
// ---------------------------------------------------------------------------

func TestBuffer_EmptyOnNew(t *testing.T) {
	buf := newBuffer()
	assert.True(t, buf.empty())
	assert.Equal(t, 0, buf.size)
}

func TestBuffer_SizeIncrements(t *testing.T) {
	buf := newBuffer()
	accountID := uuid.New()
	userID := uuid.New()

	ab := &accountBatch{
		accountID: accountID,
		userID:    userID,
		txns:      []event.TransactionRecorded{{Currency: "USD"}},
	}
	buf.add(&nats.Msg{}, ab)
	assert.Equal(t, 1, buf.size)
	assert.False(t, buf.empty())

	buf.add(&nats.Msg{}, ab)
	assert.Equal(t, 2, buf.size)
}

func TestBuffer_GroupsSameAccount(t *testing.T) {
	buf := newBuffer()
	accountID := uuid.New()
	userID := uuid.New()

	ab := &accountBatch{
		accountID: accountID,
		userID:    userID,
		txns:      []event.TransactionRecorded{{Currency: "USD"}},
	}
	buf.add(&nats.Msg{}, ab)
	buf.add(&nats.Msg{}, ab)

	assert.Len(t, buf.batches, 1)
	assert.Equal(t, 2, len(buf.batches[accountID].txns))
}

func TestBuffer_TwoAccounts(t *testing.T) {
	buf := newBuffer()
	a1, a2 := uuid.New(), uuid.New()
	u := uuid.New()

	buf.add(&nats.Msg{}, &accountBatch{accountID: a1, userID: u, txns: []event.TransactionRecorded{{}}})
	buf.add(&nats.Msg{}, &accountBatch{accountID: a2, userID: u, txns: []event.TransactionRecorded{{}}})

	assert.Len(t, buf.batches, 2)
	assert.Equal(t, 2, buf.size)
}

func TestBuffer_BadMsgsDoNotAffectSize(t *testing.T) {
	buf := newBuffer()
	buf.addBad(&nats.Msg{})
	buf.addBad(&nats.Msg{})

	assert.Equal(t, 0, buf.size)
	assert.False(t, buf.empty(), "empty() is false when there are bad msgs")
	assert.Len(t, buf.badMsgs, 2)
}

// ---------------------------------------------------------------------------
// processBatch tests
// ---------------------------------------------------------------------------

func TestProcessBatch_SingleTxnCallsSingle(t *testing.T) {
	h := &stubHandler{}
	c := newTestConsumer(h)
	accountID := uuid.New()
	userID := uuid.New()

	b := &accountBatch{
		accountID: accountID,
		userID:    userID,
		txns:      []event.TransactionRecorded{{Type: "buy", Currency: "USD"}},
	}
	err := c.processBatch(context.Background(), b)
	require.NoError(t, err)

	assert.Equal(t, 1, h.singleCount(), "single-txn batch must call HandleRecordTransaction")
	assert.Equal(t, 0, h.batchCount())
}

func TestProcessBatch_MultiTxnCallsBatch(t *testing.T) {
	h := &stubHandler{}
	c := newTestConsumer(h)
	accountID := uuid.New()
	userID := uuid.New()

	b := &accountBatch{
		accountID: accountID,
		userID:    userID,
		txns: []event.TransactionRecorded{
			{Type: "buy", Currency: "USD"},
			{Type: "buy", Currency: "USD"},
			{Type: "sell", Currency: "USD"},
		},
	}
	err := c.processBatch(context.Background(), b)
	require.NoError(t, err)

	assert.Equal(t, 0, h.singleCount())
	assert.Equal(t, 1, h.batchCount(), "multi-txn batch must call HandleRecordTransactionBatch")
	assert.Equal(t, 3, len(h.batchCalls[0].txns))
}

// ---------------------------------------------------------------------------
// flush() tests
// ---------------------------------------------------------------------------

func TestFlush_HandlerSuccessCallsHandler(t *testing.T) {
	h := &stubHandler{}
	c := newTestConsumer(h)
	accountID := uuid.New()
	userID := uuid.New()

	buf := newBuffer()
	msg := &nats.Msg{Data: []byte(`{}`)}
	ab := &accountBatch{
		accountID: accountID,
		userID:    userID,
		txns:      []event.TransactionRecorded{{Type: "buy", Currency: "USD"}},
	}
	buf.add(msg, ab)

	c.flush(context.Background(), buf)
	assert.Equal(t, 1, h.singleCount(), "handler must be called on successful flush")
}

func TestFlush_HandlerErrorDoesNotPanic(t *testing.T) {
	h := &stubHandler{failWith: errors.New("db down")}
	c := newTestConsumer(h)
	accountID := uuid.New()
	userID := uuid.New()

	buf := newBuffer()
	ab := &accountBatch{
		accountID: accountID,
		userID:    userID,
		txns:      []event.TransactionRecorded{{Type: "buy", Currency: "USD"}},
	}
	buf.add(&nats.Msg{}, ab)

	// Must not panic — Nak on nil sub returns ErrMsgNoReply which is ignored
	assert.NotPanics(t, func() {
		c.flush(context.Background(), buf)
	})
}

func TestFlush_BadMsgsDoNotPanic(t *testing.T) {
	h := &stubHandler{}
	c := newTestConsumer(h)
	buf := newBuffer()
	buf.addBad(&nats.Msg{})
	buf.addBad(&nats.Msg{})

	// Nak on msgs with nil Sub returns ErrMsgNoReply — must not panic
	assert.NotPanics(t, func() {
		c.flush(context.Background(), buf)
	})
	// No handler calls for bad msgs
	assert.Equal(t, 0, h.singleCount())
}

func TestFlush_MixedGoodAndBad(t *testing.T) {
	h := &stubHandler{}
	c := newTestConsumer(h)
	accountID := uuid.New()
	userID := uuid.New()

	buf := newBuffer()
	buf.addBad(&nats.Msg{})
	ab := &accountBatch{
		accountID: accountID,
		userID:    userID,
		txns:      []event.TransactionRecorded{{Type: "buy", Currency: "USD"}},
	}
	buf.add(&nats.Msg{}, ab)

	assert.NotPanics(t, func() {
		c.flush(context.Background(), buf)
	})
	assert.Equal(t, 1, h.singleCount(), "good msg must still be processed despite bad msgs")
}
