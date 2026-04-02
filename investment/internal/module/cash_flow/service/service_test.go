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

	"mallow/investment/internal/module/cash_flow/domain"
	"mallow/investment/internal/module/cash_flow/repository"
)

// mockCashFlowRepo is a mock implementation of repository.Repository.
type mockCashFlowRepo struct {
	mock.Mock
}

func (m *mockCashFlowRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioCashFlow, error) {
	args := m.Called(ctx, userID, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.PortfolioCashFlow), args.Error(1)
}

// setupCashFlowService creates a cashFlowSvc with mock repo for testing.
func setupCashFlowService() (*cashFlowSvc, *mockCashFlowRepo) {
	mockRepo := new(mockCashFlowRepo)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &cashFlowSvc{
		repo:   mockRepo,
		logger: logger.With("component", "cash_flow.service"),
	}
	return svc, mockRepo
}

func TestCashFlowList(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()

	t.Run("success returns cash flows", func(t *testing.T) {
		svc, mockRepo := setupCashFlowService()

		filter := repository.ListFilter{FlowType: "dividend", Limit: 10, Offset: 0}
		now := time.Now().UTC()
		expected := []domain.PortfolioCashFlow{
			{
				ID:            uuid.New(),
				AccountID:     uuid.New(),
				UserID:        userID,
				FlowType:      "dividend",
				Amount:        125.50,
				Currency:      "USD",
				Symbol:        "AAPL",
				Description:   "Q4 dividend",
				SourceEventID: uuid.New(),
				OccurredAt:    now,
				CreatedAt:     now,
			},
			{
				ID:            uuid.New(),
				AccountID:     uuid.New(),
				UserID:        userID,
				FlowType:      "dividend",
				Amount:        80.00,
				Currency:      "USD",
				Symbol:        "MSFT",
				Description:   "Q4 dividend",
				SourceEventID: uuid.New(),
				OccurredAt:    now,
				CreatedAt:     now,
			},
		}

		// mock.Anything for ctx because tracer.Start derives a child context.
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return(expected, nil)

		result, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty list returns empty slice", func(t *testing.T) {
		svc, mockRepo := setupCashFlowService()

		filter := repository.ListFilter{}
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return([]domain.PortfolioCashFlow{}, nil)

		result, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		assert.Empty(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo := setupCashFlowService()

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
		svc, mockRepo := setupCashFlowService()

		filter := repository.ListFilter{
			FlowType: "deposit",
			Limit:    50,
			Offset:   100,
		}
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return([]domain.PortfolioCashFlow{}, nil)

		_, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty flow type passes through to repo", func(t *testing.T) {
		svc, mockRepo := setupCashFlowService()

		// Empty FlowType means "all" — service does not add a default.
		filter := repository.ListFilter{FlowType: "", Limit: 20, Offset: 0}
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return([]domain.PortfolioCashFlow{}, nil)

		_, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}
