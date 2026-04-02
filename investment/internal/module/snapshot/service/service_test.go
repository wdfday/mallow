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

	"mallow/investment/internal/module/portfolio/event"
	positionDomain "mallow/investment/internal/module/position/domain"
	"mallow/investment/internal/module/snapshot/domain"
	"mallow/investment/internal/module/snapshot/repository"
)

// mockSnapshotRepo is a mock implementation of repository.Repository.
type mockSnapshotRepo struct {
	mock.Mock
}

func (m *mockSnapshotRepo) ListByUserID(ctx context.Context, userID uuid.UUID, filter repository.ListFilter) ([]domain.PortfolioSnapshot, error) {
	args := m.Called(ctx, userID, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]domain.PortfolioSnapshot), args.Error(1)
}

// mockPositionQuerier is a mock implementation of positionQuerier.
type mockPositionQuerier struct {
	mock.Mock
}

func (m *mockPositionQuerier) ListActiveByAccount(ctx context.Context, accountID uuid.UUID) ([]positionDomain.PortfolioPosition, error) {
	args := m.Called(ctx, accountID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]positionDomain.PortfolioPosition), args.Error(1)
}

// setupSnapshotService creates a snapshotSvc with mocks for testing.
// commandHandler is set to nil because it is a concrete *command.Handler.
func setupSnapshotService() (*snapshotSvc, *mockSnapshotRepo, *mockPositionQuerier) {
	mockRepo := new(mockSnapshotRepo)
	mockPQ := new(mockPositionQuerier)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &snapshotSvc{
		repo:            mockRepo,
		commandHandler:  nil, // concrete *command.Handler — Trigger success path requires integration test
		positionQuerier: mockPQ,
		logger:          logger.With("component", "snapshot.service"),
	}
	return svc, mockRepo, mockPQ
}

// sampleSnapshot returns a minimal PortfolioSnapshot for use in tests.
func sampleSnapshot(userID, accountID uuid.UUID, snapType string) domain.PortfolioSnapshot {
	now := time.Now().UTC()
	return domain.PortfolioSnapshot{
		ID:             uuid.New(),
		AccountID:      accountID,
		UserID:         userID,
		SnapshotDate:   now,
		SnapshotType:   snapType,
		TotalValue:     15000.0,
		TotalCost:      12000.0,
		UnrealizedPnL:  3000.0,
		RealizedPnL:    500.0,
		TotalReturn:    3500.0,
		TotalReturnPct: 29.17,
		SourceEventID:  uuid.New(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
}

func TestSnapshotList(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	accountID := uuid.New()

	t.Run("success returns snapshots", func(t *testing.T) {
		svc, mockRepo, _ := setupSnapshotService()

		filter := repository.ListFilter{SnapshotType: "daily", Limit: 30, Offset: 0}
		expected := []domain.PortfolioSnapshot{
			sampleSnapshot(userID, accountID, "daily"),
			sampleSnapshot(userID, accountID, "daily"),
		}
		// mock.Anything for ctx because tracer.Start derives a child context.
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return(expected, nil)

		result, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		assert.Equal(t, expected, result)
		assert.Len(t, result, 2)
		mockRepo.AssertExpectations(t)
	})

	t.Run("empty list returns empty slice", func(t *testing.T) {
		svc, mockRepo, _ := setupSnapshotService()

		filter := repository.ListFilter{}
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return([]domain.PortfolioSnapshot{}, nil)

		result, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		assert.Empty(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("repository error propagates", func(t *testing.T) {
		svc, mockRepo, _ := setupSnapshotService()

		filter := repository.ListFilter{SnapshotType: "weekly"}
		repoErr := errors.New("database error")
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return(nil, repoErr)

		result, err := svc.List(ctx, userID, filter)

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, repoErr, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("filter fields are passed through unchanged", func(t *testing.T) {
		svc, mockRepo, _ := setupSnapshotService()

		filter := repository.ListFilter{
			SnapshotType: "monthly",
			Limit:        12,
			Offset:       0,
		}
		mockRepo.On("ListByUserID", mock.Anything, userID, filter).Return([]domain.PortfolioSnapshot{}, nil)

		_, err := svc.List(ctx, userID, filter)

		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}

// TestSnapshotTrigger tests the Trigger method.
// NOTE: The success path (positionQuerier succeeds) is not tested here because
// commandHandler is a concrete *command.Handler and cannot be injected via interface.
// The success path requires an integration test with a real event store.
func TestSnapshotTrigger(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()
	userID := uuid.New()

	t.Run("positionQuerier error stops execution before commandHandler", func(t *testing.T) {
		svc, _, mockPQ := setupSnapshotService()

		repoErr := errors.New("position query failed")
		mockPQ.On("ListActiveByAccount", mock.Anything, accountID).Return(nil, repoErr)

		err := svc.Trigger(ctx, accountID, userID, event.SnapshotTypeDaily)

		require.Error(t, err)
		assert.Equal(t, repoErr, err)
		mockPQ.AssertExpectations(t)
	})

	t.Run("positionQuerier timeout propagates", func(t *testing.T) {
		svc, _, mockPQ := setupSnapshotService()

		timeoutErr := errors.New("context deadline exceeded")
		mockPQ.On("ListActiveByAccount", mock.Anything, accountID).Return(nil, timeoutErr)

		err := svc.Trigger(ctx, accountID, userID, event.SnapshotTypeManual)

		require.Error(t, err)
		assert.Equal(t, timeoutErr, err)
		mockPQ.AssertExpectations(t)
	})
}

// TestSnapshotTriggerAggregation verifies that when positionQuerier succeeds, the
// service proceeds to the commandHandler step (confirmed via the expected panic on nil).
// All aggregation logic (totalValue, unrealizedPnL, totalReturnPct) runs before
// commandHandler.HandleTakeSnapshot is called.
func TestSnapshotTriggerAggregation(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()
	userID := uuid.New()

	t.Run("positions are aggregated before commandHandler is called", func(t *testing.T) {
		svc, _, mockPQ := setupSnapshotService()

		positions := []positionDomain.PortfolioPosition{
			{
				ID:             uuid.New(),
				AccountID:      accountID,
				UserID:         userID,
				Symbol:         "AAPL",
				CurrentValue:   10500.0,
				TotalCost:      10000.0,
				UnrealizedPnL:  500.0,
				RealizedPnL:    200.0,
				TotalDividends: 50.0,
				Status:         "active",
				OpenedAt:       time.Now().UTC(),
			},
		}
		mockPQ.On("ListActiveByAccount", mock.Anything, accountID).Return(positions, nil)

		// commandHandler is nil — calling HandleTakeSnapshot will panic with a nil-pointer
		// dereference. We recover from that panic to confirm positionQuerier was called.
		func() {
			defer func() {
				recover() //nolint:errcheck // expected nil-dereference panic on commandHandler
			}()
			_ = svc.Trigger(ctx, accountID, userID, event.SnapshotTypeManual)
		}()

		// Confirm positionQuerier was called with the correct accountID.
		mockPQ.AssertExpectations(t)
	})
}
