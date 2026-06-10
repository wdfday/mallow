package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"gateway/internal/metrics"
)

// RateLimiter returns a per-IP fixed-window rate limiter backed by Redis.
//
// Each IP is allowed requestsPerMinute requests per 60-second window.
// The window is tracked with a Redis key "rl:{ip}" using INCR + EXPIRE.
//
// Loopback addresses (127.0.0.1, ::1) are exempt — they can only originate
// from the local host (dev server, integration tests) and would otherwise
// share one key and quickly exhaust the budget.
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

		// Local traffic (loopback or private RFC-1918/Docker bridge) is never
		// an external client — exempt it entirely so dev and Docker Desktop
		// host IPs (e.g. 192.168.65.1) don't share one exhaustible bucket.
		if parsed := net.ParseIP(ip); parsed != nil && (parsed.IsLoopback() || parsed.IsPrivate()) {
			c.Next()
			return
		}

		key := fmt.Sprintf("rl:%s", ip)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 200*time.Millisecond)
		defer cancel()

		// INCR + EXPIRE in a pipeline so the TTL is always set even if the
		// previous EXPIRE timed out (avoids a permanent no-TTL key that never
		// resets and locks out the IP forever).
		pipe := rdb.Pipeline()
		incrCmd := pipe.Incr(ctx, key)
		pipe.Expire(ctx, key, time.Minute)
		if _, err := pipe.Exec(ctx); err != nil {
			slog.Warn("rate limiter redis error, allowing request", "ip", ip, "err", err)
			c.Next()
			return
		}
		count := incrCmd.Val()
		if count > int64(requestsPerMinute) {
			metrics.RateLimitBlocked()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, errResp(
				http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "rate limit exceeded",
			))
			return
		}

		c.Next()
	}
}
