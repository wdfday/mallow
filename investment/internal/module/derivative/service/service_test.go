package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"mallow/investment/internal/module/derivative/domain"
	"mallow/investment/internal/module/derivative/repository"
)

// mockDerivativeRepo is a mock implementation of repository.Repository.
type mockDerivativeRepo struct {
	mock.Mock
}

func (m *mockDerivativeRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.DerivativePosition, error) {
	args := m.Called(ctx, userID, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.DerivativePosition), args.Error(1)
}

// setupDerivativeService creates a derivativeSvc with mock repo for testing.
func setupDerivativeService() (*derivativeSvc, *mockDerivativeRepo) {
	mockRepo := new(mockDerivativeRepo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &derivativeSvc{
		repo:   mockRepo,
		logger: logger.With("component", "derivative.service"),
	}
	return svc, mockRepo
}

// sampleDerivativePosition returns a minimal DerivativePosition for use in tests.
func sampleDerivativePosition(userID, accountID uuid.UUID, symbol, status string) domain.DerivativePosition {
	now := time.Now().UTC()
	liqPrice := decimal.NewFromFloat(45000.0)
	return domain.DerivativePosition{
		ID:               uuid.New(),
		AccountID:        accountID,
		UserID:           userID,
		Symbol:           symbol,
		Underlying:       "BTC",
		InstrumentType:   "perp",
		Side:             "long",
		Currency:         "USD",
		Quantity:         decimal.NewFromFloat(1.0),
		EntryPrice:       decimal.NewFromFloat(50000.0),
		CurrentPrice:     decimal.NewFromFloat(52000.0),
		Leverage:         10.0,
		MarginUsed:       decimal.NewFromFloat(5000.0),
		LiquidationPrice: &liqPrice,
		ContractSize:     decimal.NewFromFloat(1.0),
		UnrealizedPnL:    decimal.NewFromFloat(2000.0),
		Status:           status,
		OpenedAt:         now,
		OpenEventID:      uuid.New(),
		UpdatedAt:        now,
	}
}

func TestDerivativeList(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	accountID := uuid.New()

	t.Run("success returns derivative positions", func(t *testing.T) {
		svc, mockRepo := setupDerivativeService()

		expected := []domain.DerivativePosition{
			sampleDerivativePosition(userID, accountID, "BTCUSDT-PERP", "open"),
			sampleDerivativePosition(userID, accountID, "ETHUSDT-PERP", "open"),
		}
		// mock.Anything for ctx because tracer.Start derives a child context.
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "open"}).Return(expected, nil)

		result, err := svc.List(ctx, userID, "open")

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		assert.Len(t, result, 2)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty list returns empty slice", func(t *testing.T) {
		svc, mockRepo := setupDerivativeService()

		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "open"}).Return([]domain.DerivativePosition{}, nil)

		result, err := svc.List(ctx, userID, "open")

		require.NoError(t, err)
		assert.Empty(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty status defaults to open", func(t *testing.T) {
		svc, mockRepo := setupDerivativeService()

		// When caller passes "" the service must substitute "open".
		expected := []domain.DerivativePosition{sampleDerivativePosition(userID, accountID, "BTCUSDT-PERP", "open")}
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "open"}).Return(expected, nil)

		result, err := svc.List(ctx, userID, "")

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		// Confirm mock was called with "open", not "".
		mockRepo.AssertExpectations(t)
	})

	t.Run("closed status is passed through", func(t *testing.T) {
		svc, mockRepo := setupDerivativeService()

		expected := []domain.DerivativePosition{sampleDerivativePosition(userID, accountID, "BTCUSDT-PERP", "closed")}
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "closed"}).Return(expected, nil)

		result, err := svc.List(ctx, userID, "closed")

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupDerivativeService()

		repoErr := errors.New("database error")
		mockRepo.On("ListByUserID", mock.Anything, userID, repository.ListFilter{Status: "open"}).Return(nil, repoErr)

		result, err := svc.List(ctx, userID, "open")

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, repoErr, err)
		mockRepo.AssertExpectations(t)
	})
}
