package middleware

import "github.com/gin-gonic/gin"

// trustedHeaders are headers that downstream services use to identify the
// authenticated caller. They MUST be stripped from every inbound client request
// before any upstream sees them, otherwise a client can impersonate any user by
// simply including these headers in their request.
//
// On protected routes InjectUserHeaders re-populates them from the validated JWT.
// On public routes (auth, swagger) the headers are left absent — upstream
// services must not trust them on unauthenticated endpoints.
var trustedHeaders = []string{
	"X-User-ID",
	"X-User-Role",
	"X-User-Email",
}

// StripTrustedHeaders removes caller-identity headers that downstream services
// trust. Apply this as a global middleware (before JWTAuth and InjectUserHeaders)
// so that even public routes cannot be exploited via header injection.
func StripTrustedHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, h := range trustedHeaders {
			c.Request.Header.Del(h)
		}
		c.Next()
	}
}
