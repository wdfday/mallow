package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	authDomain "mallow/identity/internal/module/auth/domain"
)

// ==================== Mock Token Blacklist Repository ====================

type MockTokenBlacklistRepo struct {
	mock.Mock
}

func (m *MockTokenBlacklistRepo) Add(ctx context.Context, token string, userID uuid.UUID, reason string, expiresAt time.Time) error {
	args := m.Called(ctx, token, userID, reason, expiresAt)
	return args.Error(0)
}

func (m *MockTokenBlacklistRepo) IsBlacklisted(ctx context.Context, token string) (bool, error) {
	args := m.Called(ctx, token)
	return args.Bool(0), args.Error(1)
}

func (m *MockTokenBlacklistRepo) IsRevokedByFields(ctx context.Context, sid, userID string) (bool, error) {
	args := m.Called(ctx, sid, userID)
	return args.Bool(0), args.Error(1)
}

func (m *MockTokenBlacklistRepo) CleanupExpired(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockTokenBlacklistRepo) BlacklistAllUserTokens(ctx context.Context, userID uuid.UUID, reason string) error {
	args := m.Called(ctx, userID, reason)
	return args.Error(0)
}

func (m *MockTokenBlacklistRepo) RevokeSession(ctx context.Context, sid string, expiresAt time.Time) error {
	args := m.Called(ctx, sid, expiresAt)
	return args.Error(0)
}

var _ authDomain.ITokenBlacklistRepository = (*MockTokenBlacklistRepo)(nil)

// ==================== Test Setup ====================

func setupInternalHandlerTest() (*gin.Engine, *MockTokenBlacklistRepo) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	mockRepo := new(MockTokenBlacklistRepo)
	h := NewInternalHandler(mockRepo)
	h.RegisterRoutes(router, "test-service-secret")

	return router, mockRepo
}

// ==================== Tests ====================

func TestInternalHandlerCheckRevoked(t *testing.T) {
	t.Run("Success - not blacklisted", func(t *testing.T) {
		router, mockRepo := setupInternalHandlerTest()

		mockRepo.On("IsRevokedByFields", mock.Anything, "sid-1", "user-1").Return(false, nil)

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/internal/blacklist/check?sid=sid-1&sub=user-1", nil)
		req.Header.Set("X-Service-Secret", "test-service-secret")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"blacklisted":false}`, w.Body.String())
		mockRepo.AssertExpectations(t)
	})

	t.Run("Success - blacklisted", func(t *testing.T) {
		router, mockRepo := setupInternalHandlerTest()

		mockRepo.On("IsRevokedByFields", mock.Anything, "sid-2", "user-2").Return(true, nil)

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/internal/blacklist/check?sid=sid-2&sub=user-2", nil)
		req.Header.Set("X-Service-Secret", "test-service-secret")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.JSONEq(t, `{"blacklisted":true}`, w.Body.String())
		mockRepo.AssertExpectations(t)
	})

	t.Run("Error - repository failure", func(t *testing.T) {
		router, mockRepo := setupInternalHandlerTest()

		mockRepo.On("IsRevokedByFields", mock.Anything, "sid-3", "user-3").
			Return(false, errors.New("redis unavailable"))

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/internal/blacklist/check?sid=sid-3&sub=user-3", nil)
		req.Header.Set("X-Service-Secret", "test-service-secret")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		mockRepo.AssertExpectations(t)
	})

	t.Run("Error - missing service secret is rejected by middleware", func(t *testing.T) {
		router, mockRepo := setupInternalHandlerTest()

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/internal/blacklist/check?sid=sid-1&sub=user-1", nil)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		mockRepo.AssertNotCalled(t, "IsRevokedByFields", mock.Anything, mock.Anything, mock.Anything)
	})
}
