package projection

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/investment/internal/module/portfolio/event"
)

// ---- CashFlowProjector tests ----

// TestCashFlowProjector_DividendInsert verifies a dividend event inserts with flowType="dividend".
func TestCashFlowProjector_DividendInsert(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeDividend,
		Symbol:          "AAPL",
		Currency:        "USD",
		Amount:          25.0,
		Notes:           "Q1 dividend",
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 10, payload)

	// GORM Create with postgres RETURNING clause → ExpectQuery
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_cash_flows"`)).
		WithArgs(
			accountID,
			userID,
			"dividend",
			25.0,
			"USD",
			"AAPL",
			"Q1 dividend",
			ev.ID,
			txDate,
			sqlmock.AnyArg(), // created_at
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(newID))
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCashFlowProjector_DepositInsert verifies a deposit event inserts with flowType="deposit".
func TestCashFlowProjector_DepositInsert(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeDeposit,
		Currency:        "USD",
		Amount:          5000.0,
		Notes:           "wire transfer",
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 1, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_cash_flows"`)).
		WithArgs(
			accountID,
			userID,
			"deposit",
			5000.0,
			"USD",
			sqlmock.AnyArg(), // symbol (empty string)
			"wire transfer",
			ev.ID,
			txDate,
			sqlmock.AnyArg(), // created_at
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(newID))
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCashFlowProjector_WithdrawalInsert verifies a withdrawal event inserts with flowType="withdrawal".
func TestCashFlowProjector_WithdrawalInsert(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeWithdrawal,
		Currency:        "USD",
		Amount:          1000.0,
		Notes:           "monthly withdrawal",
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 2, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_cash_flows"`)).
		WithArgs(
			accountID,
			userID,
			"withdrawal",
			1000.0,
			"USD",
			sqlmock.AnyArg(), // symbol (empty string)
			"monthly withdrawal",
			ev.ID,
			txDate,
			sqlmock.AnyArg(), // created_at
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(newID))
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCashFlowProjector_FeeInsert verifies a fee event inserts with flowType="fee".
func TestCashFlowProjector_FeeInsert(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeFee,
		Currency:        "USD",
		Amount:          9.99,
		Notes:           "management fee",
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 3, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_cash_flows"`)).
		WithArgs(
			accountID,
			userID,
			"fee",
			9.99,
			"USD",
			sqlmock.AnyArg(), // symbol (empty string)
			"management fee",
			ev.ID,
			txDate,
			sqlmock.AnyArg(), // created_at
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(newID))
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCashFlowProjector_BuyNoDBCall verifies a buy event produces no cash flow row.
func TestCashFlowProjector_BuyNoDBCall(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeBuy,
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        10,
		PricePerUnit:    150.0,
		Amount:          1500.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 5, payload)

	// buy type falls through to default return nil — no DB call expected
	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCashFlowProjector_SellNoDBCall verifies a sell event produces no cash flow row.
func TestCashFlowProjector_SellNoDBCall(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeSell,
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        5,
		PricePerUnit:    160.0,
		Amount:          800.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 6, payload)

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCashFlowProjector_DerivativeOpenNoDBCall verifies derivative_open produces no cash flow row.
func TestCashFlowProjector_DerivativeOpenNoDBCall(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeDerivativeOpen,
		Symbol:          "BTCUSDT-PERP",
		Currency:        "USDT",
		Amount:          500.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 7, payload)

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCashFlowProjector_AccountInitializedNoDBCall verifies AccountInitialized is ignored.
func TestCashFlowProjector_AccountInitializedNoDBCall(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()

	payload := event.AccountInitialized{
		UserID:        userID,
		InitializedAt: time.Now().UTC(),
	}
	ev := makeEvent(t, event.EventTypeAccountInitialized, accountID, 1, payload)

	// EventType is not TransactionRecorded — no DB call expected
	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestCashFlowProjector_IdempotentDuplicateSourceEventID verifies ON CONFLICT DO NOTHING returns no error.
func TestCashFlowProjector_IdempotentDuplicateSourceEventID(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewCashFlowProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeDeposit,
		Currency:        "USD",
		Amount:          3000.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 1, payload)

	// ON CONFLICT DO NOTHING — RETURNING returns empty result set, no error
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_cash_flows"`)).
		WithArgs(
			accountID,
			userID,
			"deposit",
			3000.0,
			"USD",
			sqlmock.AnyArg(), // symbol
			sqlmock.AnyArg(), // description
			ev.ID,
			txDate,
			sqlmock.AnyArg(), // created_at
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"})) // no rows on conflict
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
