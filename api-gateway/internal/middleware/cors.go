package middleware

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// CORS allows configured browser origins and supports credentialed auth cookies.
//
// Security note: wildcard "*" is explicitly forbidden in CORS_ORIGINS.
// Combining Access-Control-Allow-Credentials: true with a wildcard origin is
// disallowed by the CORS spec (browsers reject it) and would, if reflected,
// allow any site to make credentialed cross-origin requests.
// Operators must enumerate allowed origins explicitly.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if origin == "*" {
			// Fail loudly at startup rather than silently misconfigure CORS.
			panic(fmt.Sprintf(
				"CORS: wildcard \"*\" in CORS_ORIGINS is forbidden — "+
					"list allowed origins explicitly (got: %v)", allowedOrigins,
			))
		}
		allowed[origin] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if _, ok := allowed[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
				c.Header("Access-Control-Allow-Credentials", "true")
			}
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With")
		c.Header("Access-Control-Max-Age", "86400")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}
