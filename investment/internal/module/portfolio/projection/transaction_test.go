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

// ---- TransactionProjector tests ----

// TestTransactionProjector_InsertBuyTransaction verifies a buy event inserts a row with correct fields.
func TestTransactionProjector_InsertBuyTransaction(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewTransactionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeBuy,
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        10,
		PricePerUnit:    150.0,
		Amount:          1500.0,
		Fees:            1.5,
		Commission:      0.5,
		Tax:             0.0,
		Broker:          "alpaca",
		Source:          "manual",
		ExternalID:      "ext-001",
		Notes:           "first buy",
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 1, payload)

	// GORM Create with postgres driver uses RETURNING "id" → ExpectQuery
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_transactions"`)).
		WithArgs(
			accountID,
			userID,
			"AAPL",
			"buy",
			"USD",
			10.0,
			150.0,
			1500.0,
			1.5,
			0.5,
			0.0,
			sqlmock.AnyArg(), // realized_pnl (nil)
			"alpaca",
			"ext-001",
			"manual",
			sqlmock.AnyArg(), // bot_id (empty)
			"first buy",
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

// TestTransactionProjector_InsertSellTransaction verifies a sell event inserts a row with txType="sell".
func TestTransactionProjector_InsertSellTransaction(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewTransactionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()
	realizedGain := 50.0

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeSell,
		Symbol:          "MSFT",
		Currency:        "USD",
		Quantity:        5,
		PricePerUnit:    310.0,
		Amount:          1550.0,
		RealizedGain:    &realizedGain,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 2, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_transactions"`)).
		WithArgs(
			accountID,
			userID,
			"MSFT",
			"sell",
			"USD",
			5.0,
			310.0,
			1550.0,
			0.0, // fees
			0.0, // commission
			0.0, // tax
			&realizedGain,
			sqlmock.AnyArg(), // broker
			sqlmock.AnyArg(), // external_id
			sqlmock.AnyArg(), // source
			sqlmock.AnyArg(), // bot_id
			sqlmock.AnyArg(), // notes
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

// TestTransactionProjector_InsertDividendTransaction verifies a dividend event inserts with txType="dividend".
func TestTransactionProjector_InsertDividendTransaction(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewTransactionProjector(db)

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
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 3, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_transactions"`)).
		WithArgs(
			accountID, userID,
			"AAPL", "dividend", "USD",
			0.0, 0.0, 25.0,
			0.0, 0.0, 0.0,
			sqlmock.AnyArg(), // realized_pnl
			sqlmock.AnyArg(), // broker
			sqlmock.AnyArg(), // external_id
			sqlmock.AnyArg(), // source
			sqlmock.AnyArg(), // bot_id
			sqlmock.AnyArg(), // notes
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

// TestTransactionProjector_InsertDepositTransaction verifies a deposit event inserts with txType="deposit".
func TestTransactionProjector_InsertDepositTransaction(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewTransactionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeDeposit,
		Currency:        "USD",
		Amount:          10000.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 4, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_transactions"`)).
		WithArgs(
			accountID, userID,
			sqlmock.AnyArg(), // symbol (empty string)
			"deposit", "USD",
			0.0, 0.0, 10000.0,
			0.0, 0.0, 0.0,
			sqlmock.AnyArg(), // realized_pnl
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
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

// TestTransactionProjector_AccountInitializedNoInsert verifies AccountInitialized is skipped.
func TestTransactionProjector_AccountInitializedNoInsert(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewTransactionProjector(db)

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

// TestTransactionProjector_IdempotentDuplicateSourceEventID verifies ON CONFLICT DO NOTHING returns no error.
func TestTransactionProjector_IdempotentDuplicateSourceEventID(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewTransactionProjector(db)

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
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 1, payload)

	// ON CONFLICT DO NOTHING → empty RETURNING result, no error
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_transactions"`)).
		WithArgs(
			accountID, userID,
			"AAPL", "buy", "USD",
			10.0, 150.0, 1500.0,
			0.0, 0.0, 0.0,
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			ev.ID,
			txDate,
			sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"})) // no rows returned on conflict
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
