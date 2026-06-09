package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/gin-gonic/gin"
)

const serviceSecretHeader = "X-Service-Secret"

// ServiceAuth returns a middleware that validates the X-Service-Secret header.
// Used to protect internal service-to-service endpoints.
//
// Security note: comparison uses crypto/subtle.ConstantTimeCompare to prevent
// timing-based secret enumeration attacks. A blank secret causes immediate
// rejection (fail-closed) to avoid accidentally open endpoints in environments
// where SERVICE_SECRET is not set.
func ServiceAuth(secret string) gin.HandlerFunc {
	secretBytes := []byte(secret)
	return func(c *gin.Context) {
		header := c.GetHeader(serviceSecretHeader)
		// Fail closed: blank secret or mismatched header → reject.
		// ConstantTimeCompare guards against timing attacks; it returns 0 if
		// lengths differ, so an empty secret never matches a non-empty header.
		if len(secret) == 0 || subtle.ConstantTimeCompare([]byte(header), secretBytes) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}
