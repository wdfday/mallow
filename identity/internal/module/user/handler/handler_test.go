package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	authDomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockUserService is a mock implementation of the IUserService interface
type MockUserService struct {
	mock.Mock
}

func (m *MockUserService) Create(ctx context.Context, user *domain.User) (*domain.User, error) {
	args := m.Called(ctx, user)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *MockUserService) GetByID(ctx context.Context, id string) (*domain.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *MockUserService) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	args := m.Called(ctx, email)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.User), args.Error(1)
}

func (m *MockUserService) List(ctx context.Context, filter domain.ListUsersFilter, pagination shared.Pagination) (shared.Page[domain.User], error) {
	args := m.Called(ctx, filter, pagination)
	return args.Get(0).(shared.Page[domain.User]), args.Error(1)
}

func (m *MockUserService) Update(ctx context.Context, user *domain.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockUserService) UpdateColumns(ctx context.Context, id string, cols map[string]any) error {
	args := m.Called(ctx, id, cols)
	return args.Error(0)
}

func (m *MockUserService) UpdatePassword(ctx context.Context, id string, passwordHash string) error {
	args := m.Called(ctx, id, passwordHash)
	return args.Error(0)
}

func (m *MockUserService) UpdateLastLogin(ctx context.Context, id string, at time.Time, ip *string) error {
	args := m.Called(ctx, id, at, ip)
	return args.Error(0)
}

func (m *MockUserService) SoftDelete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockUserService) HardDelete(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockUserService) Restore(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockUserService) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	args := m.Called(ctx, email)
	return args.Bool(0), args.Error(1)
}

func (m *MockUserService) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
	args := m.Called(ctx, id, at)
	return args.Error(0)
}

func (m *MockUserService) IncLoginAttempts(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockUserService) ResetLoginAttempts(ctx context.Context, id string) error {
	args := m.Called(ctx, id)
	return args.Error(0)
}

func (m *MockUserService) SetLockedUntil(ctx context.Context, id string, until *time.Time) error {
	args := m.Called(ctx, id, until)
	return args.Error(0)
}

func (m *MockUserService) GetByLinkedAccount(ctx context.Context, provider, providerID string) (*domain.User, error) {
	args := m.Called(ctx, provider, providerID)
	if u, ok := args.Get(0).(*domain.User); ok {
		return u, args.Error(1)
	}
	return nil, args.Error(1)
}

func (m *MockUserService) LinkAccount(ctx context.Context, userID string, account domain.LinkedAccount) error {
	args := m.Called(ctx, userID, account)
	return args.Error(0)
}

func (m *MockUserService) UnlinkAccount(ctx context.Context, userID, provider, providerID string) error {
	args := m.Called(ctx, userID, provider, providerID)
	return args.Error(0)
}

func createTestUser() *domain.User {
	now := time.Now()
	userID := uuid.New()
	return &domain.User{
		ID:               userID,
		Email:            "test@example.com",
		Password:         "hashedpassword",
		FullName:         "Test User",
		Role:             domain.UserRoleUser,
		Status:           domain.UserStatusActive,
		EmailVerified:    true,
		MFAEnabled:       false,
		LoginAttempts:    0,
		TermsAccepted:    true,
		AnalyticsConsent: true,
		LastActiveAt:     now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

func setupTestRouter() (*gin.Engine, *MockUserService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockService := new(MockUserService)
	return router, mockService
}

// Helper to set current user in context (mocking authentication)
func setCurrentUser(c *gin.Context, user *domain.User) {
	// Simulate what the auth middleware does
	authUser := authDomain.AuthUser{
		ID:       user.ID,
		Username: user.Email,
		Role:     user.Role,
	}
	c.Set("current_user", authUser)
}

func TestUserHandler_GetMe(t *testing.T) {
	t.Run("successfully get current user profile", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		mockService.On("GetByID", mock.Anything, user.ID.String()).Return(user, nil)

		router.GET("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.getMe(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/me", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.Equal(t, float64(http.StatusOK), response["status"])
		assert.NotNil(t, response["data"])

		mockService.AssertExpectations(t)
	})

	t.Run("return unauthorized when user not in context", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)

		router.GET("/api/v1/user/me", handler.getMe)

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/me", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("handle service error", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		mockService.On("GetByID", mock.Anything, user.ID.String()).
			Return(nil, errors.New("database error"))

		router.GET("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.getMe(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/me", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("handle user not found", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		mockService.On("GetByID", mock.Anything, user.ID.String()).
			Return(nil, shared.ErrUserNotFound)

		router.GET("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.getMe(c)
		})

		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/me", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		mockService.AssertExpectations(t)
	})
}

func TestUserHandler_UpdateMe(t *testing.T) {
	t.Run("successfully update user profile", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		newName := "Updated Name"
		requestBody := map[string]interface{}{
			"full_name": newName,
		}
		body, _ := json.Marshal(requestBody)

		mockService.On("UpdateColumns", mock.Anything, user.ID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["full_name"] == newName
		})).Return(nil)

		updatedUser := *user
		updatedUser.FullName = newName
		mockService.On("GetByID", mock.Anything, user.ID.String()).Return(&updatedUser, nil)

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		err := json.Unmarshal(w.Body.Bytes(), &response)
		require.NoError(t, err)

		assert.Equal(t, float64(http.StatusOK), response["status"])
		mockService.AssertExpectations(t)
	})

	t.Run("successfully update display name", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		displayName := "Display Name"
		requestBody := map[string]interface{}{
			"display_name": displayName,
		}
		body, _ := json.Marshal(requestBody)

		mockService.On("UpdateColumns", mock.Anything, user.ID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["display_name"] == displayName
		})).Return(nil)

		mockService.On("GetByID", mock.Anything, user.ID.String()).Return(user, nil)

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("successfully update phone number", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		phoneNumber := "+1234567890"
		requestBody := map[string]interface{}{
			"phone_number": phoneNumber,
		}
		body, _ := json.Marshal(requestBody)

		mockService.On("UpdateColumns", mock.Anything, user.ID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["phone_number"] == phoneNumber
		})).Return(nil)

		mockService.On("GetByID", mock.Anything, user.ID.String()).Return(user, nil)

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("clear display name with empty string", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		requestBody := map[string]interface{}{
			"display_name": "",
		}
		body, _ := json.Marshal(requestBody)

		mockService.On("UpdateColumns", mock.Anything, user.ID.String(), mock.MatchedBy(func(cols map[string]any) bool {
			return cols["display_name"] == nil
		})).Return(nil)

		mockService.On("GetByID", mock.Anything, user.ID.String()).Return(user, nil)

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("return unauthorized when user not in context", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)

		requestBody := map[string]interface{}{
			"full_name": "New Name",
		}
		body, _ := json.Marshal(requestBody)

		router.PUT("/api/v1/user/me", handler.updateMe)

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("return bad request for invalid JSON", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBufferString("invalid json"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("return bad request when full_name is empty string", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		requestBody := map[string]interface{}{
			"full_name": "",
		}
		body, _ := json.Marshal(requestBody)

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("return bad request when no fields to update", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		requestBody := map[string]interface{}{}
		body, _ := json.Marshal(requestBody)

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("handle service error during update", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		requestBody := map[string]interface{}{
			"full_name": "New Name",
		}
		body, _ := json.Marshal(requestBody)

		mockService.On("UpdateColumns", mock.Anything, user.ID.String(), mock.Anything).
			Return(errors.New("database error"))

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("handle service error during get after update", func(t *testing.T) {
		router, mockService := setupTestRouter()
		handler := NewUserHandler(mockService)
		user := createTestUser()

		requestBody := map[string]interface{}{
			"full_name": "New Name",
		}
		body, _ := json.Marshal(requestBody)

		mockService.On("UpdateColumns", mock.Anything, user.ID.String(), mock.Anything).Return(nil)
		mockService.On("GetByID", mock.Anything, user.ID.String()).
			Return(nil, errors.New("database error"))

		router.PUT("/api/v1/user/me", func(c *gin.Context) {
			setCurrentUser(c, user)
			handler.updateMe(c)
		})

		req := httptest.NewRequest(http.MethodPut, "/api/v1/user/me", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		mockService.AssertExpectations(t)
	})
}
