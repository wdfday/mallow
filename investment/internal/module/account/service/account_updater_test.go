package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"mallow/investment/internal/module/account/domain"
	accountdto "mallow/investment/internal/module/account/dto"
	"mallow/investment/internal/shared"
)

// captureMap returns a MatchedBy matcher that captures the map into dst and always matches.
func captureMap(dst *map[string]any) interface{} {
	return mock.MatchedBy(func(m map[string]any) bool {
		*dst = m
		return true
	})
}

// existingAccount returns a base account for a given user/id pair.
func existingAccount(accountID, userID string) *domain.Account {
	return &domain.Account{
		ID:     uuid.MustParse(accountID),
		UserID: uuid.MustParse(userID),
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Map key-value verification — one subtest per field
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateAccount_MapKeys(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	accountID := uuid.New().String()

	returned := &domain.Account{ID: uuid.MustParse(accountID), UserID: uuid.MustParse(userID)}

	setup := func(t *testing.T) (*accountService, *MockRepository, *map[string]any) {
		t.Helper()
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil).Once()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID, captureMap(&captured)).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(returned, nil).Once()
		return svc, mockRepo, &captured
	}

	t.Run("account_name trimmed", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		name := "  My Account  "
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountName: &name})
		require.NoError(t, err)
		assert.Equal(t, "My Account", (*captured)["account_name"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("account_type spot", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		at := "spot"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountType: &at})
		require.NoError(t, err)
		assert.Equal(t, "spot", (*captured)["account_type"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("institution_name non-empty", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		inst := "  Binance  "
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{InstitutionName: &inst})
		require.NoError(t, err)
		assert.Equal(t, "Binance", (*captured)["institution_name"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("institution_name empty string becomes nil", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		inst := "   "
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{InstitutionName: &inst})
		require.NoError(t, err)
		assert.Nil(t, (*captured)["institution_name"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("current_balance exact value", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		bal := 9_999_999.99
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{CurrentBalance: &bal})
		require.NoError(t, err)
		assert.Equal(t, bal, (*captured)["current_balance"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("available_balance stores pointer value", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		avail := 500.0
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AvailableBalance: &avail})
		require.NoError(t, err)
		stored, ok := (*captured)["available_balance"].(*float64)
		require.True(t, ok, "expected *float64")
		assert.Equal(t, avail, *stored)
		mockRepo.AssertExpectations(t)
	})

	t.Run("currency uppercased and trimmed", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		cur := " usd "
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{Currency: &cur})
		require.NoError(t, err)
		assert.Equal(t, "USD", (*captured)["currency"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("is_active true", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		active := true
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{IsActive: &active})
		require.NoError(t, err)
		assert.Equal(t, true, (*captured)["is_active"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("is_active false", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		active := false
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{IsActive: &active})
		require.NoError(t, err)
		assert.Equal(t, false, (*captured)["is_active"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("include_in_net_worth false", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		inc := false
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{IncludeInNetWorth: &inc})
		require.NoError(t, err)
		assert.Equal(t, false, (*captured)["include_in_net_worth"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("sync_status error — no last_synced_at", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		status := "error"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{SyncStatus: &status})
		require.NoError(t, err)
		assert.Equal(t, "error", (*captured)["sync_status"])
		assert.NotContains(t, *captured, "last_synced_at")
		mockRepo.AssertExpectations(t)
	})

	t.Run("sync_status active — last_synced_at set and recent", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		before := time.Now().UTC().Add(-time.Second)
		status := "active"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{SyncStatus: &status})
		require.NoError(t, err)
		assert.Equal(t, "active", (*captured)["sync_status"])
		require.Contains(t, *captured, "last_synced_at")
		ts, ok := (*captured)["last_synced_at"].(*time.Time)
		require.True(t, ok, "expected *time.Time")
		assert.True(t, ts.After(before), "last_synced_at should be recent")
		mockRepo.AssertExpectations(t)
	})

	t.Run("sync_status disconnected — no last_synced_at", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		status := "disconnected"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{SyncStatus: &status})
		require.NoError(t, err)
		assert.Equal(t, "disconnected", (*captured)["sync_status"])
		assert.NotContains(t, *captured, "last_synced_at")
		mockRepo.AssertExpectations(t)
	})

	t.Run("sync_error_message non-empty", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		msg := "connection timeout"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{SyncErrorMessage: &msg})
		require.NoError(t, err)
		assert.Equal(t, "connection timeout", (*captured)["sync_error_message"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("sync_error_message empty string becomes nil", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		msg := ""
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{SyncErrorMessage: &msg})
		require.NoError(t, err)
		assert.Nil(t, (*captured)["sync_error_message"])
		mockRepo.AssertExpectations(t)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// No-op and error paths
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateAccount_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	accountID := uuid.New().String()

	t.Run("empty request — returns existing, no UpdateColumns", func(t *testing.T) {
		svc, mockRepo := setupService()
		existing := existingAccount(accountID, userID)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existing, nil).Once()

		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{})
		require.NoError(t, err)
		assert.Equal(t, existing, result)
		mockRepo.AssertNotCalled(t, "UpdateColumns")
		mockRepo.AssertExpectations(t)
	})

	t.Run("account not found", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(nil, shared.ErrNotFound)

		name := "x"
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountName: &name})
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, shared.ErrNotFound, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("get repo internal error", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(nil, errors.New("db error"))

		name := "x"
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountName: &name})
		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("invalid account type", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil)

		bad := "invalid"
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountType: &bad})
		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertNotCalled(t, "UpdateColumns")
		mockRepo.AssertExpectations(t)
	})

	t.Run("invalid sync_status", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil)

		bad := "invalid"
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{SyncStatus: &bad})
		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertNotCalled(t, "UpdateColumns")
		mockRepo.AssertExpectations(t)
	})

	t.Run("UpdateColumns not found error", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil)
		mockRepo.On("UpdateColumns", ctx, accountID, mock.Anything).Return(shared.ErrNotFound)

		name := "x"
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountName: &name})
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Equal(t, shared.ErrNotFound, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("UpdateColumns internal error", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil)
		mockRepo.On("UpdateColumns", ctx, accountID, mock.Anything).Return(errors.New("db error"))

		name := "x"
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountName: &name})
		require.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Multiple fields — verifies correct combined map
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateAccount_MultipleFields(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	accountID := uuid.New().String()
	returned := &domain.Account{ID: uuid.MustParse(accountID)}

	t.Run("name + currency + balance + is_active", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil).Once()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID, captureMap(&captured)).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(returned, nil).Once()

		name := "Portfolio"
		cur := "usd"
		bal := 12345.67
		active := true
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{
			AccountName:    &name,
			Currency:       &cur,
			CurrentBalance: &bal,
			IsActive:       &active,
		})
		require.NoError(t, err)

		assert.Equal(t, "Portfolio", captured["account_name"])
		assert.Equal(t, "USD", captured["currency"])
		assert.Equal(t, 12345.67, captured["current_balance"])
		assert.Equal(t, true, captured["is_active"])
		assert.Len(t, captured, 4)
		mockRepo.AssertExpectations(t)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateAvailableBalance
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateAvailableBalance(t *testing.T) {
	ctx := context.Background()
	accountID := uuid.New()

	t.Run("success — correct key and value", func(t *testing.T) {
		svc, mockRepo := setupService()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID.String(), captureMap(&captured)).Return(nil)

		err := svc.UpdateAvailableBalance(ctx, accountID, decimal.NewFromFloat(8888.88))
		require.NoError(t, err)

		avail, ok := captured["available_balance"].(decimal.Decimal)
		require.True(t, ok, "expected decimal.Decimal in map")
		assert.True(t, decimal.NewFromFloat(8888.88).Equal(avail))
		assert.Len(t, captured, 1)
		mockRepo.AssertExpectations(t)
	})

	t.Run("zero balance — still updates", func(t *testing.T) {
		svc, mockRepo := setupService()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID.String(), captureMap(&captured)).Return(nil)

		err := svc.UpdateAvailableBalance(ctx, accountID, decimal.Zero)
		require.NoError(t, err)
		avail, ok := captured["available_balance"].(decimal.Decimal)
		require.True(t, ok, "expected decimal.Decimal in map")
		assert.True(t, avail.IsZero())
		mockRepo.AssertExpectations(t)
	})

	t.Run("not found error", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("UpdateColumns", ctx, accountID.String(), mock.Anything).Return(shared.ErrNotFound)

		err := svc.UpdateAvailableBalance(ctx, accountID, decimal.NewFromInt(100))
		require.Error(t, err)
		assert.Equal(t, shared.ErrNotFound, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("internal error", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("UpdateColumns", ctx, accountID.String(), mock.Anything).Return(errors.New("db error"))

		err := svc.UpdateAvailableBalance(ctx, accountID, decimal.NewFromInt(100))
		require.Error(t, err)
		mockRepo.AssertExpectations(t)
	})
}
