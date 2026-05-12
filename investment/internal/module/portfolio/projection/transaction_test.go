package projection

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/investment/internal/module/portfolio/event"
)

// ---- TransactionProjector tests ----

// Columns inserted by the projector (no broker, no trade_id):
// account_id, user_id, symbol, tx_type, currency,
// quantity, price, amount, fees, commission, tax, realized_pnl,
// external_id, source, bot_id, notes, source_event_id, tx_date, created_at

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
		Quantity:        decimal.NewFromInt(10),
		PricePerUnit:    decimal.NewFromFloat(150.0),
		Amount:          decimal.NewFromFloat(1500.0),
		Fees:            decimal.NewFromFloat(1.5),
		Commission:      decimal.NewFromFloat(0.5),
		Tax:             decimal.Zero,
		Source:          "manual",
		ExternalID:      "ext-001",
		Notes:           "first buy",
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 1, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "trades"`)).
		WithArgs(
			accountID,
			userID,
			"AAPL",
			"buy",
			"USD",
			sqlmock.AnyArg(), // quantity
			sqlmock.AnyArg(), // price
			sqlmock.AnyArg(), // amount
			sqlmock.AnyArg(), // fees
			sqlmock.AnyArg(), // commission
			sqlmock.AnyArg(), // tax
			sqlmock.AnyArg(), // realized_pnl
			"ext-001",        // external_id
			"manual",         // source
			sqlmock.AnyArg(), // bot_id
			"first buy",      // notes
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

func TestTransactionProjector_InsertSellTransaction(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewTransactionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()
	realizedGain := decimal.NewFromFloat(50.0)

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeSell,
		Symbol:          "MSFT",
		Currency:        "USD",
		Quantity:        decimal.NewFromInt(5),
		PricePerUnit:    decimal.NewFromFloat(310.0),
		Amount:          decimal.NewFromFloat(1550.0),
		RealizedGain:    &realizedGain,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 2, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "trades"`)).
		WithArgs(
			accountID, userID,
			"MSFT", "sell", "USD",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // qty, price, amount
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // fees, commission, tax
			sqlmock.AnyArg(), // realized_pnl
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
		Amount:          decimal.NewFromFloat(25.0),
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 3, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "trades"`)).
		WithArgs(
			accountID, userID,
			"AAPL", "dividend", "USD",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // qty, price, amount
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // fees, commission, tax
			sqlmock.AnyArg(), // realized_pnl
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
		Amount:          decimal.NewFromFloat(10000.0),
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 4, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "trades"`)).
		WithArgs(
			accountID, userID,
			sqlmock.AnyArg(), // symbol (empty)
			"deposit", "USD",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // qty, price, amount
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // fees, commission, tax
			sqlmock.AnyArg(),                                                       // realized_pnl
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // ext_id, source, bot_id, notes
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

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

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
		Quantity:        decimal.NewFromInt(10),
		PricePerUnit:    decimal.NewFromFloat(150.0),
		Amount:          decimal.NewFromFloat(1500.0),
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 1, payload)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "trades"`)).
		WithArgs(
			accountID, userID,
			"AAPL", "buy", "USD",
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // qty, price, amount
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // fees, commission, tax
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), // realized_pnl, ext_id, source, bot_id, notes
			ev.ID,
			txDate,
			sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"})) // no rows on conflict
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
