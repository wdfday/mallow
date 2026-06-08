package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// InjectUserHeaders reads verified JWT claims from gin context (set by JWTAuth)
// and writes X-User-ID, X-User-Role, X-User-Email into the upstream request headers.
// Must be placed after JWTAuth in the middleware chain.
//
// Security: claim values are sanitised for CRLF characters (\r, \n) before being
// written as HTTP headers to prevent header-injection attacks via crafted JWTs.
func InjectUserHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, exists := c.Get("claims")
		if !exists {
			c.Next()
			return
		}
		claims, ok := raw.(jwt.MapClaims)
		if !ok {
			c.Next()
			return
		}

		if uid := firstClaimStr(claims, "sub", "user_id"); uid != "" {
			c.Request.Header.Set("X-User-ID", sanitiseHeaderValue(uid))
		}
		if role := firstClaimStr(claims, "role"); role != "" {
			c.Request.Header.Set("X-User-Role", sanitiseHeaderValue(role))
		}
		if email := firstClaimStr(claims, "email"); email != "" {
			c.Request.Header.Set("X-User-Email", sanitiseHeaderValue(email))
		}

		c.Next()
	}
}

func firstClaimStr(claims jwt.MapClaims, keys ...string) string {
	for _, k := range keys {
		if v, ok := claims[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// sanitiseHeaderValue removes CR and LF characters from a string to prevent
// HTTP header injection (CRLF injection) attacks.
func sanitiseHeaderValue(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}
