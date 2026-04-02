package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter implements a simple token-bucket rate limiter per IP.
func RateLimiter(requestsPerMinute int) gin.HandlerFunc {
	type client struct {
		tokens    int
		lastReset time.Time
	}

	var (
		mu      sync.Mutex
		clients = make(map[string]*client)
	)

	return func(c *gin.Context) {
		ip := c.ClientIP()
		mu.Lock()

		cl, exists := clients[ip]
		if !exists {
			cl = &client{tokens: requestsPerMinute, lastReset: time.Now()}
			clients[ip] = cl
		}

		// Reset tokens every minute
		if time.Since(cl.lastReset) > time.Minute {
			cl.tokens = requestsPerMinute
			cl.lastReset = time.Now()
		}

		if cl.tokens <= 0 {
			mu.Unlock()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "rate limit exceeded",
			})
			return
		}

		cl.tokens--
		mu.Unlock()
		c.Next()
	}
}
