package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

var testLogger = slog.Default()

// ---------------------------------------------------------------------------
// CORS middleware tests
// ---------------------------------------------------------------------------

func init() {
	gin.SetMode(gin.TestMode)
}

func setupCORSRoute(origins []string) *gin.Engine {
	router := gin.New()
	router.Use(LoggerMiddleware(testLogger))
	router.Use(NewCORS(origins))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	router.OPTIONS("/test", func(c *gin.Context) {
		// OPTIONS handled by CORS middleware before reaching here
		c.JSON(http.StatusOK, nil)
	})
	return router
}

func TestNewCORS_NoOrigins_AllowsAll(t *testing.T) {
	router := setupCORSRoute(nil)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
	assert.Equal(t, "3600", w.Header().Get("Access-Control-Max-Age"))
}

func TestNewCORS_EmptySlice_AllowsAll(t *testing.T) {
	router := setupCORSRoute([]string{})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestNewCORS_SingleOrigin_SetsThatOrigin(t *testing.T) {
	router := setupCORSRoute([]string{"https://example.com"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "https://example.com", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestNewCORS_SingleEmptyOrigin_FallsBackToWildcard(t *testing.T) {
	router := setupCORSRoute([]string{"  "})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestNewCORS_MultipleOrigins_MatchesRequestOrigin(t *testing.T) {
	origins := []string{"https://a.com", "https://b.com", "https://c.com"}
	router := setupCORSRoute(origins)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://b.com")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "https://b.com", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestNewCORS_MultipleOrigins_CaseInsensitiveMatch(t *testing.T) {
	origins := []string{"https://Example.COM", "https://other.com"}
	router := setupCORSRoute(origins)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "https://example.com", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestNewCORS_MultipleOrigins_NotAllowed_FallsBackToFirst(t *testing.T) {
	origins := []string{"https://a.com", "https://b.com"}
	router := setupCORSRoute(origins)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Falls back to first origin in list
	assert.Equal(t, "https://a.com", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestNewCORS_PreflightRequest_ReturnsNoContent(t *testing.T) {
	router := gin.New()
	router.Use(LoggerMiddleware(testLogger))
	router.Use(NewCORS([]string{"https://app.com"}))
	// Must register a matching route for OPTIONS
	router.OPTIONS("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, nil) // won't be reached
	})

	req := httptest.NewRequest(http.MethodOptions, "/test", nil)
	req.Header.Set("Origin", "https://app.com")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "https://app.com", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "GET")
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "POST")
}

func TestNewCORS_SetsAllRequiredHeaders(t *testing.T) {
	router := setupCORSRoute([]string{"https://example.com"})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Methods"))
	assert.NotEmpty(t, w.Header().Get("Access-Control-Allow-Headers"))
	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
	assert.Equal(t, "3600", w.Header().Get("Access-Control-Max-Age"))
}

// ---------------------------------------------------------------------------
// CORS convenience function tests
// ---------------------------------------------------------------------------

func TestCORS_EmptyString_DefaultsToWildcard(t *testing.T) {
	router := gin.New()
	router.Use(LoggerMiddleware(testLogger))
	router.Use(CORS(""))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_CommaSeparated_ParsesMultipleOrigins(t *testing.T) {
	router := gin.New()
	router.Use(LoggerMiddleware(testLogger))
	router.Use(CORS("https://a.com, https://b.com"))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Origin", "https://b.com")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "https://b.com", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_SingleOriginString(t *testing.T) {
	router := gin.New()
	router.Use(LoggerMiddleware(testLogger))
	router.Use(CORS("https://only.com"))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, "https://only.com", w.Header().Get("Access-Control-Allow-Origin"))
}
