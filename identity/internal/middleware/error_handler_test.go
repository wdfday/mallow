package middleware

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mallow/identity/internal/shared"
)

// ---------------------------------------------------------------------------
// ErrorHandlerMiddleware tests
// ---------------------------------------------------------------------------

func setupErrorHandlerRoute(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(LoggerMiddleware(slog.Default()))
	router.Use(ErrorHandlerMiddleware())
	router.GET("/test", handler)
	return router
}

func TestErrorHandler_NoPanic_NormalResponse(t *testing.T) {
	router := setupErrorHandlerRoute(func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestErrorHandler_PanicWithString_Returns500(t *testing.T) {
	router := setupErrorHandlerRoute(func(c *gin.Context) {
		panic("something went wrong")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var body map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &body)
	require.NoError(t, err)
}

func TestErrorHandler_PanicWithAppError_ReturnsAppErrorStatus(t *testing.T) {
	router := setupErrorHandlerRoute(func(c *gin.Context) {
		panic(shared.ErrForbidden.WithDetails("message", "access denied"))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestErrorHandler_ContextError_PlainError_Returns500(t *testing.T) {
	router := setupErrorHandlerRoute(func(c *gin.Context) {
		_ = c.Error(errors.New("some internal error"))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestErrorHandler_ContextError_AppError_ReturnsMatchingStatus(t *testing.T) {
	router := setupErrorHandlerRoute(func(c *gin.Context) {
		_ = c.Error(shared.ErrNotFound.WithDetails("resource", "user"))
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// ---------------------------------------------------------------------------
// RecoveryMiddleware tests
// ---------------------------------------------------------------------------

func TestRecoveryMiddleware_PanicWithString_Returns500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(LoggerMiddleware(slog.Default()))
	router.Use(RecoveryMiddleware())
	router.GET("/test", func(c *gin.Context) {
		panic("kaboom!")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestRecoveryMiddleware_NoPanic_NormalResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(LoggerMiddleware(slog.Default()))
	router.Use(RecoveryMiddleware())
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}
