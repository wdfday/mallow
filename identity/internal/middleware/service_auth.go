package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const serviceSecretHeader = "X-Service-Secret"

// ServiceAuth returns a middleware that validates the X-Service-Secret header.
// Used to protect internal service-to-service endpoints.
func ServiceAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if secret == "" || c.GetHeader(serviceSecretHeader) != secret {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}
