package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

// MockRepository is a mock implementation of the Repository interface
type MockRepository struct {
	mock.Mock
}

func (m *MockRepository) Create(ctx context.Context, u *domain.User) error {
	args := m.Called(ctx, u)
	return args.Error(0)
}

func (m *MockRepository) GetByID(ctx context.Context, id string) (*domain.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *MockRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *MockRepository) List(ctx context.Context, f domain.ListUsersFilter, p shared.Pagination) (shared.Page[domain.User], error) {
	args := m.Called(ctx, f, p)
	return args.Get(0).(shared.Page[domain.User]), args.Error(1)
}

func (m *MockRepository) Count(ctx context.Context, f domain.ListUsersFilter) (int64, error) {
	args := m.Called(ctx, f)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockRepository) Update(ctx context.Context, u *domain.User) error {
	args := m.Called(ctx, u)
	return args.Error(0)
}

func (m *MockRepository) UpdateColumns(ctx context.Context, id string, cols map[string]any) error {
	args := m.Called(ctx, id, cols)
	return args.Error(0)
}

func (m *MockRepository) SoftDelete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockRepository) Restore(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockRepository) HardDelete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockRepository) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
	args := m.Called(ctx, id, at)
	return args.Error(0)
}

func (m *MockRepository) IncLoginAttempts(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockRepository) ResetLoginAttempts(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockRepository) SetLockedUntil(ctx context.Context, id string, until *time.Time) error {
	args := m.Called(ctx, id, until)
	return args.Error(0)
}

func (m *MockRepository) UpdateLastLogin(ctx context.Context, id string, at time.Time, ip *string) error {
	args := m.Called(ctx, id, at, ip)
	return args.Error(0)
}

func (m *MockRepository) GetByLinkedAccount(ctx context.Context, provider, providerID string) (*domain.User, error) {
	args := m.Called(ctx, provider, providerID)
	if u, ok := args.Get(0).(*domain.User); ok {
		return u, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockRepository) LinkAccount(ctx context.Context, userID string, account domain.LinkedAccount) error {
	args := m.Called(ctx, userID, account)
	return args.Error(0)
}

func (m *MockRepository) UnlinkAccount(ctx context.Context, userID, provider, providerID string) error {
	args := m.Called(ctx, userID, provider, providerID)
	return args.Error(0)
}

func createTestUser() *domain.User {
	now := time.Now()
	return &domain.User{
		ID:               uuid.New(),
		Email:            "test@example.com",
		Password:         "hashedpassword",
		FullName:         "Test User",
		Role:             domain.UserRoleUser,
		Status:           domain.UserStatusPendingVerification,
		EmailVerified:    false,
		MFAEnabled:       false,
		LoginAttempts:    0,
		TermsAccepted:    true,
		AnalyticsConsent: true,
		LastActiveAt:     now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

func setupService() (*UserService, *MockRepository) {
	mockRepo := new(MockRepository)
	logger := slog.Default()
	service := &UserService{
		repo:   mockRepo,
		logger: logger,
	}
	return service, mockRepo
}

func TestUserService_Create(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully create user", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("GetByEmail", ctx, user.Email).Return(nil, shared.ErrUserNotFound)
		mockRepo.On("Create", ctx, user).Return(nil)

		result, err := service.Create(ctx, user)
		require.NoError(t, err)
		assert.NotNil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("fail when email already exists", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()
		existingUser := createTestUser()

		mockRepo.On("GetByEmail", ctx, user.Email).Return(existingUser, nil)

		result, err := service.Create(ctx, user)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "conflict")
		mockRepo.AssertExpectations(t)
	})

	t.Run("normalize email to lowercase", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()
		user.Email = "TEST@EXAMPLE.COM"

		mockRepo.On("GetByEmail", ctx, "test@example.com").Return(nil, shared.ErrUserNotFound)
		mockRepo.On("Create", ctx, user).Return(nil)

		result, err := service.Create(ctx, user)
		require.NoError(t, err)
		assert.Equal(t, "test@example.com", result.Email)
		mockRepo.AssertExpectations(t)
	})

	t.Run("handle repository error", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("GetByEmail", ctx, user.Email).Return(nil, shared.ErrUserNotFound)
		mockRepo.On("Create", ctx, user).Return(errors.New("database error"))

		result, err := service.Create(ctx, user)
		assert.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_GetByID(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully get user by ID", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("GetByID", ctx, user.ID.String()).Return(user, nil)

		result, err := service.GetByID(ctx, user.ID.String())
		require.NoError(t, err)
		assert.Equal(t, user.Email, result.Email)
		mockRepo.AssertExpectations(t)
	})

	t.Run("return error for non-existent user", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("GetByID", ctx, userID).Return(nil, shared.ErrUserNotFound)

		result, err := service.GetByID(ctx, userID)
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})

	t.Run("handle repository error", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("GetByID", ctx, userID).Return(nil, errors.New("database error"))

		result, err := service.GetByID(ctx, userID)
		assert.Error(t, err)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_GetByEmail(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully get user by email", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("GetByEmail", ctx, user.Email).Return(user, nil)

		result, err := service.GetByEmail(ctx, user.Email)
		require.NoError(t, err)
		assert.Equal(t, user.Email, result.Email)
		mockRepo.AssertExpectations(t)
	})

	t.Run("normalize email to lowercase", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("GetByEmail", ctx, "test@example.com").Return(user, nil)

		result, err := service.GetByEmail(ctx, "TEST@EXAMPLE.COM")
		require.NoError(t, err)
		assert.Equal(t, user.Email, result.Email)
		mockRepo.AssertExpectations(t)
	})

	t.Run("return error for non-existent email", func(t *testing.T) {
		service, mockRepo := setupService()

		mockRepo.On("GetByEmail", ctx, "nonexistent@example.com").Return(nil, shared.ErrUserNotFound)

		result, err := service.GetByEmail(ctx, "nonexistent@example.com")
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
		assert.Nil(t, result)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_List(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully list users", func(t *testing.T) {
		service, mockRepo := setupService()
		users := []domain.User{*createTestUser(), *createTestUser()}
		expectedPage := shared.Page[domain.User]{
			Data:         users,
			TotalItems:   2,
			CurrentPage:  1,
			ItemsPerPage: 10,
			TotalPages:   1,
		}

		filter := domain.ListUsersFilter{}
		pagination := shared.Pagination{Page: 1, PerPage: 10}

		mockRepo.On("List", ctx, filter, pagination).Return(expectedPage, nil)

		result, err := service.List(ctx, filter, pagination)
		require.NoError(t, err)
		assert.Equal(t, int64(2), result.TotalItems)
		assert.Len(t, result.Data, 2)
		mockRepo.AssertExpectations(t)
	})

	t.Run("handle repository error", func(t *testing.T) {
		service, mockRepo := setupService()
		filter := domain.ListUsersFilter{}
		pagination := shared.Pagination{Page: 1, PerPage: 10}

		mockRepo.On("List", ctx, filter, pagination).
			Return(shared.Page[domain.User]{}, errors.New("database error"))

		result, err := service.List(ctx, filter, pagination)
		assert.Error(t, err)
		assert.Empty(t, result.Data)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_Update(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully update user", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("Update", ctx, user).Return(nil)

		err := service.Update(ctx, user)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("handle repository error", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("Update", ctx, user).Return(errors.New("database error"))

		err := service.Update(ctx, user)
		assert.Error(t, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_UpdateColumns(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully update columns", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()
		updates := map[string]any{"full_name": "New Name"}

		mockRepo.On("UpdateColumns", ctx, userID, updates).Return(nil)

		err := service.UpdateColumns(ctx, userID, updates)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("return error for non-existent user", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()
		updates := map[string]any{"full_name": "New Name"}

		mockRepo.On("UpdateColumns", ctx, userID, updates).Return(shared.ErrUserNotFound)

		err := service.UpdateColumns(ctx, userID, updates)
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_UpdatePassword(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully update password", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()
		passwordHash := "newhash"

		mockRepo.On("UpdateColumns", ctx, userID, mock.MatchedBy(func(cols map[string]any) bool {
			return cols["password"] == passwordHash && cols["password_changed_at"] != nil
		})).Return(nil)

		err := service.UpdatePassword(ctx, userID, passwordHash)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_SoftDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully soft delete user", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("SoftDelete", ctx, userID).Return(nil)

		err := service.SoftDelete(ctx, userID)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("return error for non-existent user", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("SoftDelete", ctx, userID).Return(shared.ErrUserNotFound)

		err := service.SoftDelete(ctx, userID)
		assert.ErrorIs(t, err, shared.ErrUserNotFound)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_HardDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully hard delete user", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("HardDelete", ctx, userID).Return(nil)

		err := service.HardDelete(ctx, userID)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_Restore(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully restore user", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("Restore", ctx, userID).Return(nil)

		err := service.Restore(ctx, userID)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_ExistsByEmail(t *testing.T) {
	ctx := context.Background()

	t.Run("return true when email exists", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()

		mockRepo.On("GetByEmail", ctx, user.Email).Return(user, nil)

		exists, err := service.ExistsByEmail(ctx, user.Email)
		require.NoError(t, err)
		assert.True(t, exists)
		mockRepo.AssertExpectations(t)
	})

	t.Run("return false when email does not exist", func(t *testing.T) {
		service, mockRepo := setupService()

		mockRepo.On("GetByEmail", ctx, "nonexistent@example.com").Return(nil, shared.ErrUserNotFound)

		exists, err := service.ExistsByEmail(ctx, "nonexistent@example.com")
		require.NoError(t, err)
		assert.False(t, exists)
		mockRepo.AssertExpectations(t)
	})

	t.Run("handle repository error", func(t *testing.T) {
		service, mockRepo := setupService()

		mockRepo.On("GetByEmail", ctx, "test@example.com").Return(nil, errors.New("database error"))

		exists, err := service.ExistsByEmail(ctx, "test@example.com")
		assert.Error(t, err)
		assert.False(t, exists)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_MarkEmailVerified(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully mark email verified", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()
		verifiedAt := time.Now()

		mockRepo.On("MarkEmailVerified", ctx, userID, verifiedAt).Return(nil)

		err := service.MarkEmailVerified(ctx, userID, verifiedAt)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_LoginAttempts(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully increment login attempts", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("IncLoginAttempts", ctx, userID).Return(nil)

		err := service.IncLoginAttempts(ctx, userID)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})

	t.Run("successfully reset login attempts", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()

		mockRepo.On("ResetLoginAttempts", ctx, userID).Return(nil)

		err := service.ResetLoginAttempts(ctx, userID)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_SetLockedUntil(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully set locked until", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()
		lockTime := time.Now().Add(1 * time.Hour)

		mockRepo.On("SetLockedUntil", ctx, userID, &lockTime).Return(nil)

		err := service.SetLockedUntil(ctx, userID, &lockTime)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_UpdateLastLogin(t *testing.T) {
	ctx := context.Background()

	t.Run("successfully update last login", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()
		loginTime := time.Now()
		loginIP := "192.168.1.1"

		mockRepo.On("UpdateLastLogin", ctx, userID, loginTime, &loginIP).Return(nil)

		err := service.UpdateLastLogin(ctx, userID, loginTime, &loginIP)
		require.NoError(t, err)
		mockRepo.AssertExpectations(t)
	})
}

func TestUserService_AppErrorPassThrough(t *testing.T) {
	ctx := context.Background()

	t.Run("Create returns repository app error from GetByEmail", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()
		appErr := shared.NewAppError(shared.ErrCodeConflict, "Resource conflict", 409)

		mockRepo.On("GetByEmail", ctx, user.Email).Return(nil, appErr)

		result, err := service.Create(ctx, user)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, appErr)
		mockRepo.AssertNotCalled(t, "Create", mock.Anything, mock.Anything)
		mockRepo.AssertExpectations(t)
	})

	t.Run("Create returns repository app error from Create", func(t *testing.T) {
		service, mockRepo := setupService()
		user := createTestUser()
		appErr := shared.NewAppError(shared.ErrCodeConflict, "Resource conflict", 409).WithDetails("field", "email")

		mockRepo.On("GetByEmail", ctx, user.Email).Return(nil, shared.ErrUserNotFound)
		mockRepo.On("Create", ctx, user).Return(appErr)

		result, err := service.Create(ctx, user)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, appErr)
		mockRepo.AssertExpectations(t)
	})

	t.Run("UpdateColumns returns repository app error directly", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()
		updates := map[string]any{"email": "dup@example.com"}
		appErr := shared.NewAppError(shared.ErrCodeConflict, "Resource conflict", 409).WithDetails("field", "email")

		mockRepo.On("UpdateColumns", ctx, userID, updates).Return(appErr)

		err := service.UpdateColumns(ctx, userID, updates)
		assert.ErrorIs(t, err, appErr)
		mockRepo.AssertExpectations(t)
	})

	t.Run("LinkAccount returns repository app error directly", func(t *testing.T) {
		service, mockRepo := setupService()
		userID := uuid.New().String()
		appErr := shared.NewAppError(shared.ErrCodeConflict, "Resource conflict", 409).
			WithDetails("reason", "unique_violation")
		account := domain.LinkedAccount{Provider: "telegram", ProviderID: "123"}

		mockRepo.On("LinkAccount", ctx, userID, account).Return(appErr)

		err := service.LinkAccount(ctx, userID, account)
		assert.ErrorIs(t, err, appErr)
		mockRepo.AssertExpectations(t)
	})
}
