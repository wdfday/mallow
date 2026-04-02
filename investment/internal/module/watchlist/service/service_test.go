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

	"mallow/investment/internal/module/watchlist/domain"
)

// mockWatchlistRepo is a mock implementation of repository.Repository.
type mockWatchlistRepo struct {
	mock.Mock
}

func (m *mockWatchlistRepo) ListByUserID(ctx context.Context, userID uuid.UUID) ([]domain.WatchlistItem, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.WatchlistItem), args.Error(1)
}

func (m *mockWatchlistRepo) Add(ctx context.Context, item *domain.WatchlistItem) error {
	args := m.Called(ctx, item)
	return args.Error(0)
}

func (m *mockWatchlistRepo) Delete(ctx context.Context, userID uuid.UUID, symbol string) error {
	args := m.Called(ctx, userID, symbol)
	return args.Error(0)
}

// setupWatchlistService creates a watchlistSvc with mock repo for testing.
func setupWatchlistService() (*watchlistSvc, *mockWatchlistRepo) {
	mockRepo := new(mockWatchlistRepo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &watchlistSvc{
		repo:   mockRepo,
		logger: logger.With("component", "watchlist.service"),
	}
	return svc, mockRepo
}

// sampleWatchlistItem returns a minimal WatchlistItem for use in tests.
func sampleWatchlistItem(userID uuid.UUID, symbol string) domain.WatchlistItem {
	targetPrice := 200.0
	now := time.Now().UTC()
	return domain.WatchlistItem{
		ID:          uuid.New(),
		UserID:      userID,
		Symbol:      symbol,
		Name:        symbol + " Inc.",
		AssetType:   "equity",
		TargetPrice: &targetPrice,
		Notes:       "long-term hold",
		AddedAt:     now,
		UpdatedAt:   now,
	}
}

func TestWatchlistList(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()

	t.Run("success returns watchlist items", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		expected := []domain.WatchlistItem{
			sampleWatchlistItem(userID, "AAPL"),
			sampleWatchlistItem(userID, "NVDA"),
		}
		// mock.Anything for ctx because tracer.Start derives a child context.
		mockRepo.On("ListByUserID", mock.Anything, userID).Return(expected, nil)

		result, err := svc.List(ctx, userID)

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		assert.Len(t, result, 2)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty watchlist returns empty slice", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		mockRepo.On("ListByUserID", mock.Anything, userID).Return([]domain.WatchlistItem{}, nil)

		result, err := svc.List(ctx, userID)

		require.NoError(t, err)
		assert.Empty(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		repoErr := errors.New("database error")
		mockRepo.On("ListByUserID", mock.Anything, userID).Return(nil, repoErr)

		result, err := svc.List(ctx, userID)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, repoErr, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestWatchlistAdd(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()

	t.Run("success adds item", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		item := sampleWatchlistItem(userID, "TSLA")
		mockRepo.On("Add", mock.Anything, &item).Return(nil)

		err := svc.Add(ctx, &item)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("success adds item with nil target price", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		item := domain.WatchlistItem{
			ID:        uuid.New(),
			UserID:    userID,
			Symbol:    "BTC",
			AssetType: "crypto",
		}
		mockRepo.On("Add", mock.Anything, &item).Return(nil)

		err := svc.Add(ctx, &item)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		item := sampleWatchlistItem(userID, "TSLA")
		repoErr := errors.New("unique constraint violation")
		mockRepo.On("Add", mock.Anything, &item).Return(repoErr)

		err := svc.Add(ctx, &item)

		require.Error(t, err)
		assert.Equal(t, repoErr, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestWatchlistDelete(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()

	t.Run("success deletes item", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		symbol := "AAPL"
		mockRepo.On("Delete", mock.Anything, userID, symbol).Return(nil)

		err := svc.Delete(ctx, userID, symbol)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		symbol := "AAPL"
		repoErr := errors.New("record not found")
		mockRepo.On("Delete", mock.Anything, userID, symbol).Return(repoErr)

		err := svc.Delete(ctx, userID, symbol)

		require.Error(t, err)
		assert.Equal(t, repoErr, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("delete with different user IDs uses correct userID", func(t *testing.T) {
		svc, mockRepo := setupWatchlistService()

		anotherUserID := uuid.New()
		symbol := "NVDA"
		mockRepo.On("Delete", mock.Anything, anotherUserID, symbol).Return(nil)

		err := svc.Delete(ctx, anotherUserID, symbol)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}
