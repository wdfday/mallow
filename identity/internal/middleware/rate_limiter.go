package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// RateLimiter represents a rate limiter middleware
type RateLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	rate     rate.Limit
	burst    int
	cleanup  time.Duration
}

// NewRateLimiter creates a new rate limiter
// requestsPerSecond: number of requests allowed per second
// burst: maximum burst size (number of requests that can be made at once)
// cleanupInterval: how often to clean up old limiters (e.g., 5 minutes)
func NewRateLimiter(requestsPerSecond int, burst int, cleanupInterval time.Duration) *RateLimiter {
	rl := &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rate.Limit(requestsPerSecond),
		burst:    burst,
		cleanup:  cleanupInterval,
	}

	// Start cleanup goroutine
	go rl.cleanupRoutine()

	return rl
}

// getLimiter retrieves or creates a rate limiter for the given key (IP address)
func (rl *RateLimiter) getLimiter(key string) *rate.Limiter {
	rl.mu.RLock()
	limiter, exists := rl.limiters[key]
	rl.mu.RUnlock()

	if exists {
		return limiter
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists := rl.limiters[key]; exists {
		return limiter
	}

	limiter = rate.NewLimiter(rl.rate, rl.burst)
	rl.limiters[key] = limiter
	return limiter
}

// cleanupRoutine periodically removes old limiters
func (rl *RateLimiter) cleanupRoutine() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		// Clear all limiters (simple approach)
		// In production, you might want to track last access time
		rl.limiters = make(map[string]*rate.Limiter)
		rl.mu.Unlock()
	}
}

// Middleware returns a Gin middleware function for rate limiting
func (rl *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Use IP address as the key for rate limiting
		key := c.ClientIP()

		limiter := rl.getLimiter(key)

		if !limiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "rate limit exceeded",
				"message": "too many requests, please try again later",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// IPRateLimiter creates a rate limiter based on IP address
// This is a convenience function that uses default cleanup interval
func IPRateLimiter(requestsPerSecond int, burst int) gin.HandlerFunc {
	limiter := NewRateLimiter(requestsPerSecond, burst, 5*time.Minute)
	return limiter.Middleware()
}

// UserRateLimiter creates a rate limiter based on authenticated user ID
// This should be used after authentication middleware
func UserRateLimiter(requestsPerSecond int, burst int) gin.HandlerFunc {
	limiter := NewRateLimiter(requestsPerSecond, burst, 5*time.Minute)

	return func(c *gin.Context) {
		// Try to get user ID from context
		userID, exists := c.Get("user_id")
		var key string
		if exists {
			key = userID.(string)
		} else {
			// Fallback to IP if user not authenticated
			key = c.ClientIP()
		}

		rateLimiter := limiter.getLimiter(key)

		if !rateLimiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "rate limit exceeded",
				"message": "too many requests, please try again later",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// APIKeyRateLimiter creates a rate limiter based on API key
// Useful for API consumers with API keys
func APIKeyRateLimiter(requestsPerSecond int, burst int) gin.HandlerFunc {
	limiter := NewRateLimiter(requestsPerSecond, burst, 10*time.Minute)

	return func(c *gin.Context) {
		apiKey := c.GetHeader("X-API-Key")
		if apiKey == "" {
			apiKey = c.ClientIP() // Fallback to IP
		}

		rateLimiter := limiter.getLimiter(apiKey)

		if !rateLimiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":       "rate limit exceeded",
				"message":     "too many requests, please try again later",
				"retry_after": "60s",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// GlobalRateLimiter creates a single global rate limiter for all requests
// Use this for overall API protection
func GlobalRateLimiter(requestsPerSecond int, burst int) gin.HandlerFunc {
	limiter := rate.NewLimiter(rate.Limit(requestsPerSecond), burst)

	return func(c *gin.Context) {
		if !limiter.Allow() {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "service temporarily unavailable",
				"message": "server is experiencing high load, please try again later",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
