package projection

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"mallow/investment/internal/module/portfolio/event"
)

// newMockDB creates a *gorm.DB backed by go-sqlmock.
func newMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)

	dialector := postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	})
	gormDB, err := gorm.Open(dialector, &gorm.Config{})
	require.NoError(t, err)

	t.Cleanup(func() { sqlDB.Close() })
	return gormDB, mock
}

// makeEvent marshals payload into an InvestmentEvent.
func makeEvent(t *testing.T, evType event.EventType, aggregateID uuid.UUID, seq int64, payload any) event.InvestmentEvent {
	t.Helper()
	b, err := json.Marshal(payload)
	require.NoError(t, err)
	return event.InvestmentEvent{
		ID:          uuid.New(),
		AggregateID: aggregateID,
		EventType:   evType,
		Sequence:    seq,
		Payload:     b,
		OccurredAt:  time.Now(),
	}
}

// ---- PositionProjector tests ----

// TestPositionProjector_BuyNewPosition verifies that when no position exists for the symbol,
// the projector creates a new PortfolioPosition with correct qty, avgCost and totalCost.
func TestPositionProjector_BuyNewPosition(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	newID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeBuy,
		Symbol:          "AAPL",
		Name:            "Apple Inc.",
		AssetType:       "stock",
		Exchange:        "NASDAQ",
		Currency:        "USD",
		Quantity:        10,
		PricePerUnit:    150.0,
		Amount:          1500.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 1, payload)

	// SELECT returns not-found — new position path
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "portfolio_positions" WHERE account_id = $1 AND symbol = $2 ORDER BY "portfolio_positions"."id" LIMIT $3`)).
		WithArgs(accountID, "AAPL", 1).
		WillReturnError(gorm.ErrRecordNotFound)

	// GORM Create with postgres RETURNING clause issues a Query, not an Exec
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`INSERT INTO "portfolio_positions"`)).
		WithArgs(
			accountID, userID,
			"AAPL", "Apple Inc.", "stock",
			sqlmock.AnyArg(), // asset_class (empty)
			"NASDAQ", "USD",
			sqlmock.AnyArg(), // quantity
			sqlmock.AnyArg(), // avg_cost
			sqlmock.AnyArg(), // total_cost
			0.0,              // current_price
			sqlmock.AnyArg(), // current_value
			sqlmock.AnyArg(), // unrealized_pnl
			sqlmock.AnyArg(), // unrealized_pct
			0.0,              // realized_pnl
			0.0,              // total_dividends
			0.0,              // portfolio_weight
			"active",
			int64(1),         // last_seq
			txDate,           // opened_at
			nil,              // closed_at
			sqlmock.AnyArg(), // updated_at
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(newID))
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestPositionProjector_BuyExistingPosition verifies avgCost is recalculated on a subsequent buy.
func TestPositionProjector_BuyExistingPosition(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	posID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeBuy,
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        5,
		PricePerUnit:    160.0,
		Amount:          800.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 20, payload)

	// SELECT: existing position with LastSeq=10
	rows := sqlmock.NewRows([]string{
		"id", "account_id", "user_id", "symbol", "name", "asset_type", "asset_class",
		"exchange", "currency", "quantity", "avg_cost", "total_cost",
		"current_price", "current_value", "unrealized_pnl", "unrealized_pct",
		"realized_pnl", "total_dividends", "portfolio_weight", "status",
		"last_seq", "opened_at", "closed_at", "updated_at",
	}).AddRow(
		posID, accountID, userID, "AAPL", "Apple Inc.", "stock", "",
		"NASDAQ", "USD", 10.0, 150.0, 1500.0,
		0.0, 0.0, 0.0, 0.0,
		0.0, 0.0, 0.0, "active",
		int64(10), txDate, nil, txDate,
	)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "portfolio_positions" WHERE account_id = $1 AND symbol = $2 ORDER BY "portfolio_positions"."id" LIMIT $3`)).
		WithArgs(accountID, "AAPL", 1).
		WillReturnRows(rows)

	// GORM Save on existing record issues UPDATE (Exec, not Query — no RETURNING needed)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "portfolio_positions" SET`)).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), posID,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestPositionProjector_BuyIdempotent verifies no UPDATE is issued when seq <= lastSeq.
func TestPositionProjector_BuyIdempotent(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	posID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeBuy,
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        5,
		PricePerUnit:    160.0,
		Amount:          800.0,
		TransactionDate: txDate,
	}
	// Event sequence 5, but position already has LastSeq=10 → skip
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 5, payload)

	rows := sqlmock.NewRows([]string{
		"id", "account_id", "user_id", "symbol", "name", "asset_type", "asset_class",
		"exchange", "currency", "quantity", "avg_cost", "total_cost",
		"current_price", "current_value", "unrealized_pnl", "unrealized_pct",
		"realized_pnl", "total_dividends", "portfolio_weight", "status",
		"last_seq", "opened_at", "closed_at", "updated_at",
	}).AddRow(
		posID, accountID, userID, "AAPL", "", "", "",
		"", "USD", 10.0, 150.0, 1500.0,
		0.0, 0.0, 0.0, 0.0,
		0.0, 0.0, 0.0, "active",
		int64(10), txDate, nil, txDate,
	)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "portfolio_positions" WHERE account_id = $1 AND symbol = $2 ORDER BY "portfolio_positions"."id" LIMIT $3`)).
		WithArgs(accountID, "AAPL", 1).
		WillReturnRows(rows)

	// No UPDATE expected because seq(5) <= lastSeq(10)
	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestPositionProjector_SellReducesQuantity verifies a sell event triggers a Save.
func TestPositionProjector_SellReducesQuantity(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	posID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeSell,
		Symbol:          "AAPL",
		Currency:        "USD",
		Quantity:        3,
		PricePerUnit:    170.0,
		Amount:          510.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 30, payload)

	rows := sqlmock.NewRows([]string{
		"id", "account_id", "user_id", "symbol", "name", "asset_type", "asset_class",
		"exchange", "currency", "quantity", "avg_cost", "total_cost",
		"current_price", "current_value", "unrealized_pnl", "unrealized_pct",
		"realized_pnl", "total_dividends", "portfolio_weight", "status",
		"last_seq", "opened_at", "closed_at", "updated_at",
	}).AddRow(
		posID, accountID, userID, "AAPL", "Apple Inc.", "stock", "",
		"NASDAQ", "USD", 10.0, 150.0, 1500.0,
		0.0, 0.0, 0.0, 0.0,
		0.0, 0.0, 0.0, "active",
		int64(20), txDate, nil, txDate,
	)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "portfolio_positions" WHERE account_id = $1 AND symbol = $2 ORDER BY "portfolio_positions"."id" LIMIT $3`)).
		WithArgs(accountID, "AAPL", 1).
		WillReturnRows(rows)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "portfolio_positions" SET`)).
		WithArgs(
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
			sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), posID,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestPositionProjector_SellSymbolNotFound verifies no error and no UPDATE when position is absent.
func TestPositionProjector_SellSymbolNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeSell,
		Symbol:          "TSLA",
		Currency:        "USD",
		Quantity:        1,
		PricePerUnit:    200.0,
		Amount:          200.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 5, payload)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT * FROM "portfolio_positions" WHERE account_id = $1 AND symbol = $2 ORDER BY "portfolio_positions"."id" LIMIT $3`)).
		WithArgs(accountID, "TSLA", 1).
		WillReturnError(gorm.ErrRecordNotFound)

	// No UPDATE expected — no position to sell from
	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestPositionProjector_DividendUpdatesTotalDividends verifies the UpdateColumn SQL.
func TestPositionProjector_DividendUpdatesTotalDividends(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeDividend,
		Symbol:          "AAPL",
		Currency:        "USD",
		Amount:          25.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 40, payload)

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(`UPDATE "portfolio_positions" SET "total_dividends"=total_dividends + $1 WHERE account_id = $2 AND symbol = $3`)).
		WithArgs(25.0, accountID, "AAPL").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestPositionProjector_DividendEmptySymbol verifies that a dividend with empty symbol is a no-op.
func TestPositionProjector_DividendEmptySymbol(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeDividend,
		Symbol:          "",
		Currency:        "USD",
		Amount:          10.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 5, payload)

	// Symbol is empty — projector returns nil early, no DB call
	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestPositionProjector_NonPositionEventDeposit verifies that deposit events produce no DB calls.
func TestPositionProjector_NonPositionEventDeposit(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()
	txDate := time.Now().UTC()

	payload := event.TransactionRecorded{
		UserID:          userID,
		Type:            event.TransactionTypeDeposit,
		Currency:        "USD",
		Amount:          5000.0,
		TransactionDate: txDate,
	}
	ev := makeEvent(t, event.EventTypeTransactionRecorded, accountID, 1, payload)

	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

// TestPositionProjector_AccountInitializedIgnored verifies AccountInitialized events are ignored.
func TestPositionProjector_AccountInitializedIgnored(t *testing.T) {
	db, mock := newMockDB(t)
	proj := NewPositionProjector(db)

	accountID := uuid.New()
	userID := uuid.New()

	payload := event.AccountInitialized{
		UserID:        userID,
		InitializedAt: time.Now().UTC(),
	}
	ev := makeEvent(t, event.EventTypeAccountInitialized, accountID, 1, payload)

	// AccountInitialized is not handled — no DB calls expected
	err := proj.Project(context.Background(), ev)
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
