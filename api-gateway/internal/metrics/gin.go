package metrics

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Middleware records per-request metrics. It uses the matched route template
// (c.FullPath) as the route label to keep cardinality bounded — never the raw
// path. It also feeds the proxy-error and auth/ratelimit counters off the final
// status when the dedicated hooks weren't hit.
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		RequestStarted()
		start := time.Now()

		c.Next()

		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := c.Writer.Status()
		RequestFinished(c.Request.Method, route, status, float64(time.Since(start).Milliseconds()))
	}
}

// Handler serves the Prometheus exposition. Mount at GET /metrics.
func Handler(c *gin.Context) {
	c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	c.String(http.StatusOK, Render())
}
