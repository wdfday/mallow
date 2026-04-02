package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// NewCORS creates a new CORS middleware handler
func NewCORS(origins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := GetLogger(c)
		origin := "*"
		reqOrigin := c.GetHeader("Origin")

		// If no origins specified, allow all
		if len(origins) == 0 {
			origin = "*"
		} else if len(origins) == 1 {
			// Single origin
			origin = strings.TrimSpace(origins[0])
			if origin == "" {
				origin = "*"
			}
		} else {
			// Multiple origins - check if request origin is allowed
			originAllowed := false
			for _, o := range origins {
				o = strings.TrimSpace(o)
				if strings.EqualFold(o, reqOrigin) {
					origin = reqOrigin
					originAllowed = true
					break
				}
			}

			if !originAllowed && reqOrigin != "" {
				logger.Debug("CORS: Origin not in allowed list",
					"origin", reqOrigin,
					"allowed_origins", origins,
					"path", c.Request.URL.Path,
				)
				// Still use default origin (will be "*" or first in list)
				origin = origins[0]
			}
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS,PATCH")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Max-Age", "3600")

		if c.Request.Method == http.MethodOptions {
			logger.Debug("CORS preflight request",
				"origin", reqOrigin,
				"allowed_origin", origin,
				"path", c.Request.URL.Path,
			)
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// CORS creates a CORS middleware handler from a comma-separated string
func CORS(allowed string) gin.HandlerFunc {
	allowed = strings.TrimSpace(allowed)
	origins := []string{"*"}
	if allowed != "" {
		origins = strings.Split(allowed, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}
	}
	return NewCORS(origins)
}
