package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Rate limiter middleware tests
// ---------------------------------------------------------------------------

func setupRateLimitRoute(handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api", handler, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

// TestGlobalRateLimiter_UnderBurst expects all requests within burst to pass.
func TestGlobalRateLimiter_UnderBurst(t *testing.T) {
	// Allow 100 req/s, burst of 10.
	router := setupRateLimitRoute(GlobalRateLimiter(100, 10))

	// 5 requests — well under burst.
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code, "request %d should succeed", i+1)
	}
}

// TestGlobalRateLimiter_ExceedsBurst expects 429 once token bucket is empty.
func TestGlobalRateLimiter_ExceedsBurst(t *testing.T) {
	// 0 req/s (no refill), burst of 2 — third request will be rejected.
	router := setupRateLimitRoute(GlobalRateLimiter(0, 2))

	statuses := make([]int, 5)
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		statuses[i] = w.Code
	}

	// First two should pass, after that all should be 429.
	assert.Equal(t, http.StatusOK, statuses[0])
	assert.Equal(t, http.StatusOK, statuses[1])
	for i := 2; i < 5; i++ {
		assert.Equal(t, http.StatusTooManyRequests, statuses[i], "request %d should be rate-limited", i+1)
	}
}

// TestIPRateLimiter_UnderBurst expects requests under burst to pass.
func TestIPRateLimiter_UnderBurst(t *testing.T) {
	router := setupRateLimitRoute(IPRateLimiter(100, 10))

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}
}

// TestIPRateLimiter_ExceedsBurst expects 429 once per-IP bucket is exhausted.
func TestIPRateLimiter_ExceedsBurst(t *testing.T) {
	router := setupRateLimitRoute(IPRateLimiter(0, 1))

	req1 := httptest.NewRequest(http.MethodGet, "/api", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	req2 := httptest.NewRequest(http.MethodGet, "/api", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusTooManyRequests, w2.Code)
}

// TestGlobalRateLimiter_ResponseBody checks that the 429 body contains an error field.
func TestGlobalRateLimiter_ResponseBody(t *testing.T) {
	router := setupRateLimitRoute(GlobalRateLimiter(0, 0))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Contains(t, w.Body.String(), "error")
}

// TestNewRateLimiter_GetLimiter verifies per-key limiter creation.
func TestNewRateLimiter_GetLimiter(t *testing.T) {
	const cleanupSec = 60
	rl := NewRateLimiter(10, 5, cleanupSec*1e9) // use nanoseconds so cleanup doesn't trigger during test

	l1 := rl.getLimiter("192.168.1.1")
	l2 := rl.getLimiter("192.168.1.1")
	l3 := rl.getLimiter("10.0.0.1")

	assert.Same(t, l1, l2, "same key should return same limiter instance")
	assert.NotSame(t, l1, l3, "different keys should return different limiters")
}
