package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

// ---------------------------------------------------------------------------
// LinkedAccount operations: GetByLinkedAccount, LinkAccount, UnlinkAccount
// ---------------------------------------------------------------------------

func TestUserService_GetByLinkedAccount(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully get user by linked account", func(t *testing.T) {
		svc, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("GetByLinkedAccount", ctx, "telegram", "123456").Return(user, nil)

		result, err := svc.GetByLinkedAccount(ctx, "telegram", "123456")
		require.NoError(t, err)
		assert.Equal(t, user.ID, result.ID)
		mockRepo.AssertExpectations(t)
	})

	t.Run("return ErrUserNotFound when not linked", func(t *testing.T) {
		svc, mockRepo := setupService()

		mockRepo.On("GetByLinkedAccount", ctx, "telegram", "999").Return(nil, shared.ErrUserNotFound)

		result, err := svc.GetByLinkedAccount(ctx, "telegram", "999")
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("wrap repository error as internal", func(t *testing.T) {
		svc, mockRepo := setupService()

		mockRepo.On("GetByLinkedAccount", ctx, "telegram", "000").Return(nil, errors.New("db dead"))

		result, err := svc.GetByLinkedAccount(ctx, "telegram", "000")
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.True(t, shared.IsAppError(err))
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_LinkAccount(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully link account", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()
		acc := domain.LinkedAccount{
			Provider:   "telegram",
			ProviderID: "123456",
			Username:   "@alice",
			LinkedAt:   time.Now(),
		}

		mockRepo.On("LinkAccount", ctx, userID, acc).Return(nil)

		err := svc.LinkAccount(ctx, userID, acc)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("wrap repository error as internal", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()
		acc := domain.LinkedAccount{Provider: "discord", ProviderID: "999"}

		mockRepo.On("LinkAccount", ctx, userID, acc).Return(errors.New("db error"))

		err := svc.LinkAccount(ctx, userID, acc)
		assert.Error(t, err)
		assert.True(t, shared.IsAppError(err))
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_UnlinkAccount(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully unlink account", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("UnlinkAccount", ctx, userID, "telegram", "111").Return(nil)

		err := svc.UnlinkAccount(ctx, userID, "telegram", "111")
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("return ErrUserNotFound when user missing", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("UnlinkAccount", ctx, userID, "telegram", "111").Return(shared.ErrUserNotFound)

		err := svc.UnlinkAccount(ctx, userID, "telegram", "111")
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
		mockRepo.AssertExpectations(t)
	})

	t.Run("wrap repository error as internal", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("UnlinkAccount", ctx, userID, "telegram", "111").Return(errors.New("db down"))

		err := svc.UnlinkAccount(ctx, userID, "telegram", "111")
		assert.Error(t, err)
		assert.True(t, shared.IsAppError(err))
		mockRepo.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// UpdatePassword — test error propagation from UpdateColumns
// ---------------------------------------------------------------------------

func TestUserService_UpdatePassword_ErrorPath(t *testing.T) {
	ctx := context.Background()

	t.Run("propagate error when UpdateColumns fails", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("UpdateColumns", ctx, userID, mock.MatchedBy(func(cols map[string]any) bool {
			_, hasPassword := cols["password"]
			return hasPassword
		})).Return(errors.New("db error"))

		err := svc.UpdatePassword(ctx, userID, "newhash")
		assert.Error(t, err)
		mockRepo.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// Update — test not-found and internal error branches
// ---------------------------------------------------------------------------

func TestUserService_Update_ErrorBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("propagate ErrUserNotFound", func(t *testing.T) {
		svc, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("Update", ctx, user).Return(shared.ErrUserNotFound)

		err := svc.Update(ctx, user)
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
		mockRepo.AssertExpectations(t)
	})

	t.Run("wrap other errors as internal", func(t *testing.T) {
		svc, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("Update", ctx, user).Return(errors.New("db dead"))

		err := svc.Update(ctx, user)
		assert.Error(t, err)
		assert.True(t, shared.IsAppError(err))
		mockRepo.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// SoftDelete / HardDelete / Restore — error branches
// ---------------------------------------------------------------------------

func TestUserService_DeleteRestore_Errors(t *testing.T) {
	ctx := context.Background()

	t.Run("softdelete internal error wrapped", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("SoftDelete", ctx, userID).Return(errors.New("db err"))

		err := svc.SoftDelete(ctx, userID)
		assert.Error(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("harddelete propagates raw error", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("HardDelete", ctx, userID).Return(errors.New("db err"))

		err := svc.HardDelete(ctx, userID)
		assert.Error(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("restore propagates raw error", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("Restore", ctx, userID).Return(errors.New("not found"))

		err := svc.Restore(ctx, userID)
		assert.Error(t, err)
		mockRepo.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// UpdateColumns — internal error branch
// ---------------------------------------------------------------------------

func TestUserService_UpdateColumns_InternalError(t *testing.T) {
	ctx := context.Background()

	t.Run("wrap non-not-found error as internal", func(t *testing.T) {
		svc, mockRepo := setupService()
		userID := uuid.New().String()
		updates := map[string]any{"full_name": "X"}

		mockRepo.On("UpdateColumns", ctx, userID, updates).Return(errors.New("db timeout"))

		err := svc.UpdateColumns(ctx, userID, updates)
		assert.Error(t, err)
		assert.True(t, shared.IsAppError(err))
		mockRepo.AssertExpectations(t)
	})
}

// ---------------------------------------------------------------------------
// GetByID / GetByEmail — internal error wrapping branches
// ---------------------------------------------------------------------------

func TestUserService_GetByID_InternalErrorWrap(t *testing.T) {
	ctx := context.Background()
	svc, mockRepo := setupService()
	userID := uuid.New().String()

	mockRepo.On("GetByID", ctx, userID).Return(nil, errors.New("connection reset"))

	result, err := svc.GetByID(ctx, userID)
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, shared.IsAppError(err))
	mockRepo.AssertExpectations(t)
}

func TestUserService_GetByEmail_InternalErrorWrap(t *testing.T) {
	ctx := context.Background()
	svc, mockRepo := setupService()

	mockRepo.On("GetByEmail", ctx, "fail@example.com").Return(nil, errors.New("pg dead"))

	result, err := svc.GetByEmail(ctx, "fail@example.com")
	assert.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, shared.IsAppError(err))
	mockRepo.AssertExpectations(t)
}
