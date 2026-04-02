package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// InjectUserHeaders reads verified JWT claims from gin context (set by JWTAuth)
// and writes X-User-ID, X-User-Role, X-User-Email into the upstream request headers.
// Must be placed after JWTAuth in the middleware chain.
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
			c.Request.Header.Set("X-User-ID", uid)
		}
		if role := firstClaimStr(claims, "role"); role != "" {
			c.Request.Header.Set("X-User-Role", role)
		}
		if email := firstClaimStr(claims, "email"); email != "" {
			c.Request.Header.Set("X-User-Email", email)
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
