package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	authDomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/shared"
)

func setupSessionTest(withUser bool, userID uuid.UUID) (*gin.Engine, *MockSessionService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	mockSessionService := new(MockSessionService)
	h := NewSessionHandler(mockSessionService)

	g := router.Group("/api/v1/auth/sessions")
	if withUser {
		g.Use(setCurrentUser(userID))
	}
	{
		g.GET("", h.list)
		g.DELETE("/:sid", h.revoke)
		g.DELETE("", h.revokeAll)
	}

	return router, mockSessionService
}

func TestSessionHandlerList(t *testing.T) {
	t.Run("Success - lists sessions and marks current", func(t *testing.T) {
		userID := uuid.New()
		router, mockService := setupSessionTest(true, userID)

		sessions := []authDomain.Session{
			{SID: "sid-1", IP: "1.1.1.1", UserAgent: "ua-1", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
			{SID: "sid-2", IP: "2.2.2.2", UserAgent: "ua-2", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)},
		}
		mockService.On("ListSessions", mock.Anything, userID.String()).Return(sessions, nil)

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/sessions", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)

		var response map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		data, ok := response["data"].([]interface{})
		assert.True(t, ok)
		assert.Len(t, data, 2)

		mockService.AssertExpectations(t)
	})

	t.Run("Error - unauthorized when user missing from context", func(t *testing.T) {
		router, mockService := setupSessionTest(false, uuid.Nil)

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/sessions", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		mockService.AssertNotCalled(t, "ListSessions", mock.Anything, mock.Anything)
	})

	t.Run("Error - service failure propagates", func(t *testing.T) {
		userID := uuid.New()
		router, mockService := setupSessionTest(true, userID)

		mockService.On("ListSessions", mock.Anything, userID.String()).
			Return(nil, shared.ErrInternal.WithError(assert.AnError))

		req, _ := http.NewRequest(http.MethodGet, "/api/v1/auth/sessions", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		mockService.AssertExpectations(t)
	})
}

func TestSessionHandlerRevoke(t *testing.T) {
	t.Run("Success - revokes session by sid", func(t *testing.T) {
		userID := uuid.New()
		router, mockService := setupSessionTest(true, userID)

		mockService.On("RevokeSession", mock.Anything, "sid-1", userID.String()).Return(nil)

		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/auth/sessions/sid-1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("Error - unauthorized when user missing from context", func(t *testing.T) {
		router, mockService := setupSessionTest(false, uuid.Nil)

		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/auth/sessions/sid-1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		mockService.AssertNotCalled(t, "RevokeSession", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("Error - session not found", func(t *testing.T) {
		userID := uuid.New()
		router, mockService := setupSessionTest(true, userID)

		mockService.On("RevokeSession", mock.Anything, "missing-sid", userID.String()).
			Return(shared.ErrNotFound.WithDetails("resource", "session"))

		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/auth/sessions/missing-sid", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusNotFound, w.Code)
		mockService.AssertExpectations(t)
	})
}

func TestSessionHandlerRevokeAll(t *testing.T) {
	t.Run("Success - revokes all sessions", func(t *testing.T) {
		userID := uuid.New()
		router, mockService := setupSessionTest(true, userID)

		mockService.On("RevokeAllSessions", mock.Anything, userID.String()).Return(nil)

		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/auth/sessions", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		mockService.AssertExpectations(t)
	})

	t.Run("Error - unauthorized when user missing from context", func(t *testing.T) {
		router, mockService := setupSessionTest(false, uuid.Nil)

		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/auth/sessions", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
		mockService.AssertNotCalled(t, "RevokeAllSessions", mock.Anything, mock.Anything)
	})

	t.Run("Error - service failure propagates", func(t *testing.T) {
		userID := uuid.New()
		router, mockService := setupSessionTest(true, userID)

		mockService.On("RevokeAllSessions", mock.Anything, userID.String()).
			Return(shared.ErrInternal.WithError(assert.AnError))

		req, _ := http.NewRequest(http.MethodDelete, "/api/v1/auth/sessions", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		mockService.AssertExpectations(t)
	})
}
