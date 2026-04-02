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

	"mallow/investment/internal/module/position/domain"
	"mallow/investment/internal/module/position/repository"
)

// mockPositionRepo is a mock implementation of repository.Repository.
type mockPositionRepo struct {
	mock.Mock
}

func (m *mockPositionRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioPosition, error) {
	args := m.Called(ctx, userID, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.PortfolioPosition), args.Error(1)
}

func (m *mockPositionRepo) ListByAccountID(ctx context.Context, accountID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioPosition, error) {
	args := m.Called(ctx, accountID, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.PortfolioPosition), args.Error(1)
}

func (m *mockPositionRepo) GetBySymbol(ctx context.Context, userID uuid.UUID, symbol string) (*domain.PortfolioPosition, error) {
	args := m.Called(ctx, userID, symbol)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.PortfolioPosition), args.Error(1)
}

// setupPositionService creates a positionSvc with mock repo for testing.
func setupPositionService() (*positionSvc, *mockPositionRepo) {
	mockRepo := new(mockPositionRepo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &positionSvc{
		repo:   mockRepo,
		logger: logger.With("component", "position.service"),
	}
	return svc, mockRepo
}

// samplePosition returns a minimal PortfolioPosition for use in tests.
func samplePosition(userID, accountID uuid.UUID, symbol, status string) domain.PortfolioPosition {
	now := time.Now().UTC()
	return domain.PortfolioPosition{
		ID:            uuid.New(),
		AccountID:     accountID,
		UserID:        userID,
		Symbol:        symbol,
		Name:          symbol + " Inc.",
		AssetType:     "equity",
		Currency:      "USD",
		Quantity:      10,
		AvgCost:       100.0,
		TotalCost:     1000.0,
		CurrentPrice:  105.0,
		CurrentValue:  1050.0,
		UnrealizedPnL: 50.0,
		Status:        status,
		OpenedAt:      now,
		UpdatedAt:     now,
	}
}

func TestPositionList(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	accountID := uuid.New()

	t.Run("empty statusFilter defaults to active", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		expected := []domain.PortfolioPosition{samplePosition(userID, accountID, "AAPL", "active")}
		// mock.Anything for ctx because tracer.Start derives a child context.
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "active"}).Return(expected, nil)

		result, err := svc.List(ctx, userID, "")

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		// Confirm the mock was called with "active", not "".
		mockRepo.AssertExpectations(t)
	})

	t.Run("explicit status is passed through", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		expected := []domain.PortfolioPosition{samplePosition(userID, accountID, "TSLA", "closed")}
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "closed"}).Return(expected, nil)

		result, err := svc.List(ctx, userID, "closed")

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty list returns empty slice", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "active"}).Return([]domain.PortfolioPosition{}, nil)

		result, err := svc.List(ctx, userID, "active")

		require.NoError(t, err)
		assert.Empty(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		repoErr := errors.New("database error")
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "active"}).Return(nil, repoErr)

		result, err := svc.List(ctx, userID, "active")

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, repoErr, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestPositionListActive(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	accountID := uuid.New()

	t.Run("ListActive calls List with active status", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		expected := []domain.PortfolioPosition{samplePosition(userID, accountID, "AAPL", "active")}
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "active"}).Return(expected, nil)

		result, err := svc.ListActive(ctx, userID)

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("ListActive propagates repo error", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		repoErr := errors.New("connection reset")
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "active"}).Return(nil, repoErr)

		result, err := svc.ListActive(ctx, userID)

		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

func TestPositionListActiveByAccount(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	accountID := uuid.New()

	t.Run("success with account ID", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		expected := []domain.PortfolioPosition{
			samplePosition(userID, accountID, "BTC", "active"),
			samplePosition(userID, accountID, "ETH", "active"),
		}
		mockRepo.On("ListByAccountID", mock.Anything, accountID, repository.ListFilter{Status: "active"}).Return(expected, nil)

		result, err := svc.ListActiveByAccount(ctx, accountID)

		require.NoError(t, err)
		assert.Len(t, result, 2)
		assert.Equal(t, expected, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty account returns empty slice", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		mockRepo.On("ListByAccountID", mock.Anything, accountID, repository.ListFilter{Status: "active"}).Return([]domain.PortfolioPosition{}, nil)

		result, err := svc.ListActiveByAccount(ctx, accountID)

		require.NoError(t, err)
		assert.Empty(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		repoErr := errors.New("query timeout")
		mockRepo.On("ListByAccountID", mock.Anything, accountID, repository.ListFilter{Status: "active"}).Return(nil, repoErr)

		result, err := svc.ListActiveByAccount(ctx, accountID)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, repoErr, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestPositionGetBySymbol(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	accountID := uuid.New()

	t.Run("success returns position", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		symbol := "AAPL"
		expected := samplePosition(userID, accountID, symbol, "active")
		mockRepo.On("GetBySymbol", mock.Anything, userID, symbol).Return(&expected, nil)

		result, err := svc.GetBySymbol(ctx, userID, symbol)

		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, symbol, result.Symbol)
		mockRepo.AssertExpectations(t)
	})

	t.Run("not found returns nil and error", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		symbol := "UNKNOWN"
		notFoundErr := errors.New("record not found")
		mockRepo.On("GetBySymbol", mock.Anything, userID, symbol).Return(nil, notFoundErr)

		result, err := svc.GetBySymbol(ctx, userID, symbol)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, notFoundErr, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupPositionService()

		symbol := "AAPL"
		repoErr := errors.New("database unavailable")
		mockRepo.On("GetBySymbol", mock.Anything, userID, symbol).Return(nil, repoErr)

		result, err := svc.GetBySymbol(ctx, userID, symbol)

		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}
