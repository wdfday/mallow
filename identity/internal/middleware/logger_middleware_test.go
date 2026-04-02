package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// LoggerMiddleware tests
// ---------------------------------------------------------------------------

func TestLoggerMiddleware_InjectsLogger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.Default()

	var captured *slog.Logger
	router := gin.New()
	router.Use(LoggerMiddleware(logger))
	router.GET("/test", func(c *gin.Context) {
		captured = GetLogger(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, logger, captured)
}

func TestLoggerMiddleware_NilLogger_FallsBackToDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var captured *slog.Logger
	router := gin.New()
	router.Use(LoggerMiddleware(nil))
	router.GET("/test", func(c *gin.Context) {
		captured = GetLogger(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	// nil stored → GetLogger returns slog.Default()
	assert.NotNil(t, captured)
}

func TestGetLogger_NoLoggerInContext_ReturnsDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var captured *slog.Logger
	router := gin.New()
	// No LoggerMiddleware applied
	router.GET("/test", func(c *gin.Context) {
		captured = GetLogger(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, slog.Default(), captured)
}

func TestGetLogger_WrongTypeInContext_ReturnsDefault(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var captured *slog.Logger
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(LoggerKey, "not a logger") // wrong type
		c.Next()
	})
	router.GET("/test", func(c *gin.Context) {
		captured = GetLogger(c)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, slog.Default(), captured)
}
