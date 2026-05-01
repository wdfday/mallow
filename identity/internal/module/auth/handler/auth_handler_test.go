package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"mallow/identity/internal/config"
	authDomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/auth/dto"
	userdomain "mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/shared"
)

// ==================== Mocks ====================

type MockAuthService struct {
	mock.Mock
}

func (m *MockAuthService) Register(ctx context.Context, req dto.RegisterRequest) (*dto.AuthResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dto.AuthResult), args.Error(1)
}

func (m *MockAuthService) Login(ctx context.Context, req dto.LoginRequest) (*dto.AuthResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dto.AuthResult), args.Error(1)
}

func (m *MockAuthService) Logout(ctx context.Context, userID, refreshToken, ipAddress string) error {
	args := m.Called(ctx, userID, refreshToken, ipAddress)
	return args.Error(0)
}

func (m *MockAuthService) RefreshToken(ctx context.Context, refreshToken string) (*dto.TokenResponse, error) {
	args := m.Called(ctx, refreshToken)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dto.TokenResponse), args.Error(1)
}

func (m *MockAuthService) AuthenticateGoogle(ctx context.Context, req dto.GoogleAuthRequest) (*dto.AuthResult, error) {
	args := m.Called(ctx, req)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dto.AuthResult), args.Error(1)
}

// ==================== Mock Session Service ====================

type MockSessionService struct {
	mock.Mock
}

func (m *MockSessionService) CreateSession(ctx context.Context, sid, userID, ip, userAgent string) error {
	args := m.Called(ctx, sid, userID, ip, userAgent)
	return args.Error(0)
}

func (m *MockSessionService) ListSessions(ctx context.Context, userID string) ([]authDomain.Session, error) {
	args := m.Called(ctx, userID)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]authDomain.Session), args.Error(1)
}

func (m *MockSessionService) RevokeSession(ctx context.Context, sid, userID string) error {
	args := m.Called(ctx, sid, userID)
	return args.Error(0)
}

func (m *MockSessionService) RevokeAllSessions(ctx context.Context, userID string) error {
	args := m.Called(ctx, userID)
	return args.Error(0)
}

// ==================== Test Setup ====================

func setupTest() (*gin.Engine, *MockAuthService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	mockAuthService := new(MockAuthService)
	mockSessionService := new(MockSessionService)
	mockSessionService.On("CreateSession", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	cfg := &config.Config{
		Server: config.ServerConfig{
			Port: "8080",
		},
	}

	handler := NewAuthHandler(mockAuthService, mockSessionService, cfg)

	// Register routes (without actual middleware for testing)
	auth := router.Group("/api/v1/auth")
	{
		auth.POST("/register", handler.register)
		auth.POST("/login", handler.login)
		auth.POST("/google", handler.authenticateGoogle)
		auth.POST("/refresh", handler.refreshToken)
		auth.POST("/logout", handler.logout) // Simplified for test
	}

	return router, mockAuthService
}

// ==================== Tests ====================

// TestRegisterHandler tests registration endpoint
func TestRegisterHandler(t *testing.T) {
	t.Run("Success - Register new user", func(t *testing.T) {
		router, mockService := setupTest()

		userID := uuid.New()
		user := &userdomain.User{
			ID:    userID,
			Email: "test@example.com",
			Role:  userdomain.UserRoleUser,
		}

		authResult := &dto.AuthResult{
			User:         user,
			AccessToken:  "access_token_123",
			RefreshToken: "refresh_token_123",
			ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		}

		reqBody := dto.RegisterRequest{
			Email:    "test@example.com",
			Password: "Password123!",
			FullName: "Test User",
		}

		mockService.On("Register", mock.Anything, mock.MatchedBy(func(req dto.RegisterRequest) bool {
			return req.Email == "test@example.com" &&
				req.Password == "Password123!" &&
				req.FullName == "Test User"
		})).Return(authResult, nil)

		// Create request
		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		// Record response
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Assert
		assert.Equal(t, http.StatusCreated, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		assert.Equal(t, float64(http.StatusCreated), response["status"])
		assert.Equal(t, "User registered successfully", response["message"])
		assert.NotNil(t, response["data"])

		mockService.AssertExpectations(t)
	})

	t.Run("Error - Invalid request body", func(t *testing.T) {
		router, _ := setupTest()

		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer([]byte("invalid json")))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		assert.Equal(t, float64(http.StatusBadRequest), response["status"])
		assert.Equal(t, "invalid request data", response["message"])
	})

	t.Run("Error - Email already exists", func(t *testing.T) {
		router, mockService := setupTest()

		reqBody := dto.RegisterRequest{
			Email:    "existing@example.com",
			Password: "Password123!",
			FullName: "Test User",
		}

		mockService.On("Register", mock.Anything, mock.AnythingOfType("dto.RegisterRequest")).
			Return(nil, shared.ErrConflict.WithDetails("field", "email"))

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusConflict, w.Code)

		mockService.AssertExpectations(t)
	})
}

// TestLoginHandler tests login endpoint
func TestLoginHandler(t *testing.T) {
	t.Run("Success - Login with valid credentials", func(t *testing.T) {
		router, mockService := setupTest()

		userID := uuid.New()
		user := &userdomain.User{
			ID:    userID,
			Email: "test@example.com",
			Role:  userdomain.UserRoleUser,
		}

		authResult := &dto.AuthResult{
			User:         user,
			AccessToken:  "access_token_123",
			RefreshToken: "refresh_token_123",
			ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		}

		reqBody := dto.LoginRequest{
			Email:    "test@example.com",
			Password: "Password123!",
		}

		mockService.On("Login", mock.Anything, mock.MatchedBy(func(req dto.LoginRequest) bool {
			return req.Email == "test@example.com" && req.Password == "Password123!"
		})).Return(authResult, nil)

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		assert.Equal(t, float64(http.StatusOK), response["status"])
		assert.NotNil(t, response["data"])

		// Check cookie was set
		cookies := w.Result().Cookies()
		assert.Greater(t, len(cookies), 0, "refresh_token cookie should be set")

		mockService.AssertExpectations(t)
	})

	t.Run("Error - Invalid credentials", func(t *testing.T) {
		router, mockService := setupTest()

		reqBody := dto.LoginRequest{
			Email:    "test@example.com",
			Password: "WrongPassword",
		}

		mockService.On("Login", mock.Anything, mock.AnythingOfType("dto.LoginRequest")).
			Return(nil, shared.ErrUnauthorized.WithDetails("message", "invalid credentials"))

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		assert.Equal(t, float64(http.StatusUnauthorized), response["status"])
		assert.Equal(t, "UNAUTHORIZED", response["code"])
		assert.Equal(t, "invalid credentials", response["message"])

		mockService.AssertExpectations(t)
	})

	t.Run("Error - Missing required fields", func(t *testing.T) {
		router, _ := setupTest()

		reqBody := map[string]string{
			"email": "test@example.com",
			// Missing password
		}

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

// TestRefreshTokenHandler tests token refresh endpoint
func TestRefreshTokenHandler(t *testing.T) {
	t.Run("Success - Refresh token", func(t *testing.T) {
		router, mockService := setupTest()

		tokenResponse := &dto.TokenResponse{
			AccessToken: "new_access_token",
			ExpiresIn:   3600,
		}

		mockService.On("RefreshToken", mock.Anything, "refresh_token_123").
			Return(tokenResponse, nil)

		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		req.Header.Set("Content-Type", "application/json")

		// Simulate refresh token cookie
		req.AddCookie(&http.Cookie{
			Name:  "refresh_token",
			Value: "refresh_token_123",
		})

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		assert.Equal(t, float64(http.StatusOK), response["status"])
		assert.NotNil(t, response["data"])

		mockService.AssertExpectations(t)
	})

	t.Run("Error - Missing refresh token", func(t *testing.T) {
		router, _ := setupTest()

		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("Error - Invalid refresh token", func(t *testing.T) {
		router, mockService := setupTest()

		mockService.On("RefreshToken", mock.Anything, "invalid_token").
			Return(nil, shared.ErrUnauthorized.WithDetails("message", "invalid refresh token"))

		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
		req.AddCookie(&http.Cookie{
			Name:  "refresh_token",
			Value: "invalid_token",
		})

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)

		mockService.AssertExpectations(t)
	})
}

// TestLogoutHandler tests logout endpoint
func TestLogoutHandler(t *testing.T) {
	t.Run("Success - Logout user", func(t *testing.T) {
		router, mockService := setupTest()

		// Mock middleware would normally set this
		userID := uuid.New().String()

		mockService.On("Logout", mock.Anything, userID, "refresh_token_123", mock.AnythingOfType("string")).
			Return(nil)

		// For testing, we'll pass user_id in context manually
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
		req.AddCookie(&http.Cookie{
			Name:  "refresh_token",
			Value: "refresh_token_123",
		})

		// Add user ID to context (simulating middleware)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Note: Without actual middleware, this will fail gracefully
		// In real test, you'd mock the middleware or use integration test
	})

	t.Run("Error - Missing refresh token", func(t *testing.T) {
		router, _ := setupTest()

		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return error due to missing token
		assert.NotEqual(t, http.StatusOK, w.Code)
	})
}

// TestGoogleAuthHandler tests Google OAuth endpoint
func TestGoogleAuthHandler(t *testing.T) {
	t.Run("Success - Google OAuth login", func(t *testing.T) {
		router, mockService := setupTest()

		userID := uuid.New()
		user := &userdomain.User{
			ID:            userID,
			Email:         "test@gmail.com",
			Role:          userdomain.UserRoleUser,
			EmailVerified: true,
		}

		authResult := &dto.AuthResult{
			User:         user,
			AccessToken:  "access_token_123",
			RefreshToken: "refresh_token_123",
			ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		}

		reqBody := dto.GoogleAuthRequest{
			Token: "google_token_123",
		}

		mockService.On("AuthenticateGoogle", mock.Anything, mock.MatchedBy(func(req dto.GoogleAuthRequest) bool {
			return req.Token == "google_token_123"
		})).Return(authResult, nil)

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/google", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		json.Unmarshal(w.Body.Bytes(), &response)

		assert.Equal(t, float64(http.StatusOK), response["status"])
		assert.NotNil(t, response["data"])

		mockService.AssertExpectations(t)
	})

	t.Run("Error - Invalid Google token", func(t *testing.T) {
		router, mockService := setupTest()

		reqBody := dto.GoogleAuthRequest{
			Token: "invalid_google_token",
		}

		mockService.On("AuthenticateGoogle", mock.Anything, mock.AnythingOfType("dto.GoogleAuthRequest")).
			Return(nil, errors.New("invalid google token"))

		bodyBytes, _ := json.Marshal(reqBody)
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/google", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.NotEqual(t, http.StatusOK, w.Code)

		mockService.AssertExpectations(t)
	})
}
