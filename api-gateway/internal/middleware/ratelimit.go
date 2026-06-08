package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RateLimiter returns a per-IP fixed-window rate limiter backed by Redis.
//
// Each IP is allowed requestsPerMinute requests per 60-second window.
// The window is tracked with a Redis key "rl:{ip}" using INCR + EXPIRE.
//
// If Redis is unavailable, the request is allowed through (fail-open with a
// warning log). This is the same availability-over-security tradeoff used by
// the JWT blacklist check: rate limiting is a QoS guard, not a security gate,
// so denying all traffic on Redis failure is worse than the alternative.
//
// Redis-backed enforcement means the limit is shared across all gateway replicas
// — unlike the previous in-memory map which allowed N× the configured rate in
// multi-replica deployments.
func RateLimiter(rdb *redis.Client, requestsPerMinute int) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		key := fmt.Sprintf("rl:%s", ip)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 100*time.Millisecond)
		defer cancel()

		count, err := rdb.Incr(ctx, key).Result()
		if err != nil {
			slog.Warn("rate limiter redis error, allowing request", "ip", ip, "err", err)
			c.Next()
			return
		}
		if count == 1 {
			// First request in this window — set the expiry.
			// Ignore the error: worst case the key never expires and the IP
			// gets an extra free window (not a security issue).
			rdb.Expire(ctx, key, time.Minute)
		}
		if count > int64(requestsPerMinute) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, errResp(
				http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "rate limit exceeded",
			))
			return
		}

		c.Next()
	}
}
