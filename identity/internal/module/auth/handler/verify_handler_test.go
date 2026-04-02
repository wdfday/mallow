package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	authDomain "mallow/identity/internal/module/auth/domain"
	dto "mallow/identity/internal/module/auth/dto"
	"mallow/identity/internal/shared"
)

// MockVerificationService
type MockVerificationService struct {
	mock.Mock
}

func (m *MockVerificationService) SendVerificationEmail(ctx context.Context, userID, ipAddress, userAgent string) error {
	args := m.Called(ctx, userID, ipAddress, userAgent)
	return args.Error(0)
}

func (m *MockVerificationService) VerifyEmail(ctx context.Context, token string) error {
	args := m.Called(ctx, token)
	return args.Error(0)
}

func (m *MockVerificationService) ResendVerificationEmail(ctx context.Context, email, ipAddress, userAgent string) error {
	args := m.Called(ctx, email, ipAddress, userAgent)
	return args.Error(0)
}

func (m *MockVerificationService) CleanupExpiredTokens(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func setupVerifyTest() (*gin.Engine, *MockVerificationService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	mockService := new(MockVerificationService)

	return router, mockService
}

func TestVerifyHandler_VerifyEmail(t *testing.T) {
	t.Run("successfully verify email", func(t *testing.T) {
		router, mockService := setupVerifyTest()
		handler := NewVerifyHandler(mockService)

		router.POST("/verify-email", handler.verifyEmail)

		reqBody := dto.VerifyEmailRequest{Token: "valid_token"}
		body, _ := json.Marshal(reqBody)

		mockService.On("VerifyEmail", mock.Anything, "valid_token").Return(nil)

		req := httptest.NewRequest(http.MethodPost, "/verify-email", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("bad request for invalid body", func(t *testing.T) {
		router, _ := setupVerifyTest()
		handler := NewVerifyHandler(nil)

		router.POST("/verify-email", handler.verifyEmail)

		req := httptest.NewRequest(http.MethodPost, "/verify-email", bytes.NewBufferString("invalid"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("handle service error", func(t *testing.T) {
		router, mockService := setupVerifyTest()
		handler := NewVerifyHandler(mockService)

		router.POST("/verify-email", handler.verifyEmail)

		reqBody := dto.VerifyEmailRequest{Token: "invalid_token"}
		body, _ := json.Marshal(reqBody)

		mockService.On("VerifyEmail", mock.Anything, "invalid_token").Return(shared.ErrTokenInvalid)

		req := httptest.NewRequest(http.MethodPost, "/verify-email", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestVerifyHandler_SendVerification(t *testing.T) {
	t.Run("successfully send verification", func(t *testing.T) {
		router, mockService := setupVerifyTest()
		handler := NewVerifyHandler(mockService)
		userID := uuid.New()

		router.POST("/send-verification", func(c *gin.Context) {
			user := authDomain.AuthUser{ID: userID}
			c.Set("current_user", user)
			handler.sendVerification(c)
		})

		mockService.On("SendVerificationEmail", mock.Anything, userID.String(), mock.Anything, mock.Anything).Return(nil)

		req := httptest.NewRequest(http.MethodPost, "/send-verification", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("unauthorized if no user in context", func(t *testing.T) {
		router, _ := setupVerifyTest()
		handler := NewVerifyHandler(nil)

		router.POST("/send-verification", func(c *gin.Context) {
			handler.sendVerification(c)
		})

		req := httptest.NewRequest(http.MethodPost, "/send-verification", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("handle service error", func(t *testing.T) {
		router, mockService := setupVerifyTest()
		handler := NewVerifyHandler(mockService)
		userID := uuid.New()

		router.POST("/send-verification", func(c *gin.Context) {
			user := authDomain.AuthUser{ID: userID}
			c.Set("current_user", user)
			handler.sendVerification(c)
		})

		mockService.On("SendVerificationEmail", mock.Anything, userID.String(), mock.Anything, mock.Anything).Return(shared.ErrBadRequest)

		req := httptest.NewRequest(http.MethodPost, "/send-verification", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestVerifyHandler_ResendVerification(t *testing.T) {
	t.Run("successfully resend verification", func(t *testing.T) {
		router, mockService := setupVerifyTest()
		handler := NewVerifyHandler(mockService)

		router.POST("/resend-verification", handler.resendVerification)

		reqBody := dto.ResendVerificationRequest{Email: "test@example.com"}
		body, _ := json.Marshal(reqBody)

		mockService.On("ResendVerificationEmail", mock.Anything, "test@example.com", mock.Anything, mock.Anything).Return(nil)

		req := httptest.NewRequest(http.MethodPost, "/resend-verification", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("bad request for invalid body", func(t *testing.T) {
		router, _ := setupVerifyTest()
		handler := NewVerifyHandler(nil)

		router.POST("/resend-verification", handler.resendVerification)

		req := httptest.NewRequest(http.MethodPost, "/resend-verification", bytes.NewBufferString("invalid"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("returns success even if service fails (security)", func(t *testing.T) {
		router, mockService := setupVerifyTest()
		handler := NewVerifyHandler(mockService)

		router.POST("/resend-verification", handler.resendVerification)

		reqBody := dto.ResendVerificationRequest{Email: "test@example.com"}
		body, _ := json.Marshal(reqBody)

		mockService.On("ResendVerificationEmail", mock.Anything, "test@example.com", mock.Anything, mock.Anything).Return(shared.ErrUserNotFound)

		req := httptest.NewRequest(http.MethodPost, "/resend-verification", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})
}
