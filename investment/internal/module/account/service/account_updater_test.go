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

	"mallow/investment/internal/module/account/domain"
	accountdto "mallow/investment/internal/module/account/dto"
	internalService "mallow/investment/internal/service"
	"mallow/investment/internal/shared"
)

// setupServiceWithEncrypt builds a service with a real encryption service,
// needed for tests that pass AccountNumber (triggers s.encrypt.Encrypt).
func setupServiceWithEncrypt(t *testing.T) (*accountService, *MockRepository) {
	t.Helper()
	enc, err := internalService.NewEncryptionService("test-encryption-key-32bytes-xxx!")
	require.NoError(t, err)
	mockRepo := new(MockRepository)
	svc := &accountService{
		repo:    mockRepo,
		encrypt: enc,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return svc, mockRepo
}

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
		ID:        uuid.MustParse(accountID),
		UserID:    uuid.MustParse(userID),
		IsPrimary: false,
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

	t.Run("account_type parsed and lowercased", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		at := "BANK"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountType: &at})
		require.NoError(t, err)
		assert.Equal(t, "bank", (*captured)["account_type"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("institution_name non-empty", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		inst := "  Vietcombank  "
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{InstitutionName: &inst})
		require.NoError(t, err)
		assert.Equal(t, "Vietcombank", (*captured)["institution_name"])
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
		// stored as *float64, dereference to compare
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

	t.Run("is_primary false — no unsetPrimary call", func(t *testing.T) {
		svc, mockRepo, captured := setup(t)
		prim := false
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{IsPrimary: &prim})
		require.NoError(t, err)
		assert.Equal(t, false, (*captured)["is_primary"])
		// ListByUserID (unsetPrimary) must NOT have been called
		mockRepo.AssertNotCalled(t, "ListByUserID")
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
// AccountNumber — encryption + masking combinations
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateAccount_AccountNumber(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	accountID := uuid.New().String()
	returned := &domain.Account{ID: uuid.MustParse(accountID), UserID: uuid.MustParse(userID)}

	t.Run("account_number only — auto-masked, encrypted", func(t *testing.T) {
		svc, mockRepo := setupServiceWithEncrypt(t)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil).Once()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID, captureMap(&captured)).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(returned, nil).Once()

		num := "1234567890"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountNumber: &num})
		require.NoError(t, err)

		// encrypted value must be non-empty and different from plain text
		enc, ok := captured["account_number_encrypted"].(string)
		require.True(t, ok)
		assert.NotEmpty(t, enc)
		assert.NotEqual(t, "1234567890", enc)

		// auto-mask: last 4 digits
		assert.Equal(t, "****7890", captured["account_number_masked"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("account_number + explicit masked — uses explicit mask", func(t *testing.T) {
		svc, mockRepo := setupServiceWithEncrypt(t)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil).Once()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID, captureMap(&captured)).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(returned, nil).Once()

		num := "1234567890"
		mask := "****-custom"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{
			AccountNumber:       &num,
			AccountNumberMasked: &mask,
		})
		require.NoError(t, err)

		// masked should come from explicit value, not auto
		assert.Equal(t, "****-custom", captured["account_number_masked"])
		// encrypted still set
		assert.NotEmpty(t, captured["account_number_encrypted"])
		mockRepo.AssertExpectations(t)
	})

	t.Run("account_number_masked only — no encryption", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil).Once()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID, captureMap(&captured)).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(returned, nil).Once()

		mask := "****1234"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountNumberMasked: &mask})
		require.NoError(t, err)

		assert.Equal(t, "****1234", captured["account_number_masked"])
		assert.NotContains(t, captured, "account_number_encrypted")
		mockRepo.AssertExpectations(t)
	})

	t.Run("account_number whitespace-only — not stored", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil).Once()

		num := "   "
		// empty request after trimming → UpdateColumns NOT called
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountNumber: &num})
		require.NoError(t, err)
		assert.NotNil(t, result) // returns existing account
		mockRepo.AssertNotCalled(t, "UpdateColumns")
		mockRepo.AssertExpectations(t)
	})

	t.Run("account_number short (≤4 chars) — fully masked", func(t *testing.T) {
		svc, mockRepo := setupServiceWithEncrypt(t)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existingAccount(accountID, userID), nil).Once()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID, captureMap(&captured)).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(returned, nil).Once()

		num := "123"
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{AccountNumber: &num})
		require.NoError(t, err)
		assert.Equal(t, "***", captured["account_number_masked"])
		mockRepo.AssertExpectations(t)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// IsPrimary — side-effect: unsetPrimaryAccount
// ─────────────────────────────────────────────────────────────────────────────

func TestUpdateAccount_IsPrimary(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New().String()
	accountID := uuid.New().String()
	returned := &domain.Account{ID: uuid.MustParse(accountID), UserID: uuid.MustParse(userID), IsPrimary: true}

	t.Run("set primary on non-primary account — unsets others first", func(t *testing.T) {
		svc, mockRepo := setupService()
		existing := existingAccount(accountID, userID) // IsPrimary = false

		otherID := uuid.New()
		otherPrimary := domain.Account{ID: otherID, UserID: uuid.MustParse(userID), IsPrimary: true}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existing, nil).Once()
		// unsetPrimaryAccount: find current primaries
		isPrim := true
		mockRepo.On("ListByUserID", ctx, userID, domain.ListAccountsFilter{IsPrimary: &isPrim}).
			Return([]domain.Account{otherPrimary}, nil)
		// unset the other one
		mockRepo.On("UpdateColumns", ctx, otherID.String(), map[string]any{"is_primary": false}).Return(nil)
		// update this account
		mockRepo.On("UpdateColumns", ctx, accountID, map[string]any{"is_primary": true}).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(returned, nil).Once()

		prim := true
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{IsPrimary: &prim})
		require.NoError(t, err)
		assert.True(t, result.IsPrimary)
		mockRepo.AssertExpectations(t)
	})

	t.Run("set primary on already-primary account — no unsetPrimary", func(t *testing.T) {
		svc, mockRepo := setupService()
		existing := &domain.Account{
			ID:        uuid.MustParse(accountID),
			UserID:    uuid.MustParse(userID),
			IsPrimary: true, // already primary
		}

		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existing, nil).Once()
		mockRepo.On("UpdateColumns", ctx, accountID, map[string]any{"is_primary": true}).Return(nil)
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(returned, nil).Once()

		prim := true
		_, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{IsPrimary: &prim})
		require.NoError(t, err)
		mockRepo.AssertNotCalled(t, "ListByUserID")
		mockRepo.AssertExpectations(t)
	})

	t.Run("unsetPrimary repo error propagates", func(t *testing.T) {
		svc, mockRepo := setupService()
		existing := existingAccount(accountID, userID)

		isPrim := true
		mockRepo.On("GetByIDAndUserID", ctx, accountID, userID).Return(existing, nil).Once()
		mockRepo.On("ListByUserID", ctx, userID, domain.ListAccountsFilter{IsPrimary: &isPrim}).
			Return(nil, errors.New("db error"))

		prim := true
		result, err := svc.UpdateAccount(ctx, accountID, userID, accountdto.UpdateAccountRequest{IsPrimary: &prim})
		require.Error(t, err)
		assert.Nil(t, result)
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
		// only these 4 keys — nothing extra
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

		err := svc.UpdateAvailableBalance(ctx, accountID, 8888.88)
		require.NoError(t, err)

		avail, ok := captured["available_balance"].(float64)
		require.True(t, ok, "expected float64 in map")
		assert.Equal(t, 8888.88, avail)

		// only one key updated
		assert.Len(t, captured, 1)
		mockRepo.AssertExpectations(t)
	})

	t.Run("zero balance — still updates", func(t *testing.T) {
		svc, mockRepo := setupService()
		var captured map[string]any
		mockRepo.On("UpdateColumns", ctx, accountID.String(), captureMap(&captured)).Return(nil)

		err := svc.UpdateAvailableBalance(ctx, accountID, 0)
		require.NoError(t, err)
		avail, ok := captured["available_balance"].(float64)
		require.True(t, ok, "expected float64 in map")
		assert.Equal(t, 0.0, avail)
		mockRepo.AssertExpectations(t)
	})

	t.Run("not found error", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("UpdateColumns", ctx, accountID.String(), mock.Anything).Return(shared.ErrNotFound)

		err := svc.UpdateAvailableBalance(ctx, accountID, 100)
		require.Error(t, err)
		assert.Equal(t, shared.ErrNotFound, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("internal error", func(t *testing.T) {
		svc, mockRepo := setupService()
		mockRepo.On("UpdateColumns", ctx, accountID.String(), mock.Anything).Return(errors.New("db error"))

		err := svc.UpdateAvailableBalance(ctx, accountID, 100)
		require.Error(t, err)
		mockRepo.AssertExpectations(t)
	})
}
