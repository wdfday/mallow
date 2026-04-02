package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"mallow/investment/internal/module/transaction/domain"
	"mallow/investment/internal/module/transaction/repository"
)

// mockTransactionRepo is a mock implementation of repository.Repository.
type mockTransactionRepo struct {
	mock.Mock
}

func (m *mockTransactionRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioTransaction, error) {
	args := m.Called(ctx, userID, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.PortfolioTransaction), args.Error(1)
}

// setupTransactionService creates a transactionSvc with mock repo and nil handler for testing.
func setupTransactionService() (*transactionSvc, *mockTransactionRepo) {
	mockRepo := new(mockTransactionRepo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &transactionSvc{
		repo:    mockRepo,
		handler: nil, // concrete *command.Handler — tested via integration tests only
		logger:  logger.With("component", "transaction.service"),
	}
	return svc, mockRepo
}

// TestTransactionList tests the List method.
// NOTE: Record tests are omitted because s.handler is a concrete *command.Handler
// that cannot be mocked via interface. Record requires an integration test with a
// real event store.
func TestTransactionList(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()

	t.Run("success returns transactions", func(t *testing.T) {
		svc, mockRepo := setupTransactionService()

		filter := repository.ListFilter{TxType: "buy", Symbol: "AAPL", Limit: 10, Offset: 0}
		now := time.Now().UTC()
		expected := []domain.PortfolioTransaction{
			{
				ID:        uuid.New(),
				AccountID: uuid.New(),
				UserID:    userID,
				Symbol:    "AAPL",
				TxType:    "buy",
				Currency:  "USD",
				Quantity:  10,
				Price:     150.0,
				Amount:    1500.0,
				TxDate:    now,
				CreatedAt: now,
			},
			{
				ID:        uuid.New(),
				AccountID: uuid.New(),
				UserID:    userID,
				Symbol:    "AAPL",
				TxType:    "buy",
				Currency:  "USD",
				Quantity:  5,
				Price:     155.0,
				Amount:    775.0,
				TxDate:    now,
				CreatedAt: now,
			},
		}

		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return(expected, nil)

		result, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty list returns empty slice", func(t *testing.T) {
		svc, mockRepo := setupTransactionService()

		filter := repository.ListFilter{}
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return([]domain.PortfolioTransaction{}, nil)

		result, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		assert.Empty(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupTransactionService()

		filter := repository.ListFilter{}
		repoErr := errors.New("database connection failed")
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return(nil, repoErr)

		result, err := svc.List(ctx, userID, filter)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, repoErr, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("filter fields are passed through unchanged", func(t *testing.T) {
		svc, mockRepo := setupTransactionService()

		filter := repository.ListFilter{
			TxType: "sell",
			Symbol: "BTCUSDT",
			Limit:  25,
			Offset: 50,
		}

		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return([]domain.PortfolioTransaction{}, nil)

		_, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("different user IDs are forwarded to repo", func(t *testing.T) {
		svc, mockRepo := setupTransactionService()

		anotherUserID := uuid.New()
		filter := repository.ListFilter{}
		mockRepo.On("ListByUserID", mock.Anything, anotherUserID, filter).Return([]domain.PortfolioTransaction{}, nil)

		_, err := svc.List(ctx, anotherUserID, filter)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}
