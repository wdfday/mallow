package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// WSBearerFromProtocol lets WebSocket clients authenticate without an
// Authorization header (the browser WebSocket API cannot set one). The client
// sends the JWT as the second Sec-WebSocket-Protocol token:
//
//	new WebSocket(url, ["bearer", "<jwt>"])
//	→ Sec-WebSocket-Protocol: bearer, <jwt>
//
// This middleware copies that token into the Authorization header so the
// existing JWTAuth middleware can validate it unchanged. It must run BEFORE
// JWTAuth. If an Authorization header is already present (non-browser clients) it
// is left untouched.
//
// The upgrader echoes "bearer" (not the token) back as the negotiated
// subprotocol so browsers accept the handshake.
func WSBearerFromProtocol() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") != "" {
			c.Next()
			return
		}
		proto := c.GetHeader("Sec-WebSocket-Protocol")
		if proto == "" {
			c.Next()
			return
		}
		parts := strings.Split(proto, ",")
		if len(parts) >= 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "bearer") {
			token := strings.TrimSpace(parts[1])
			if token != "" {
				c.Request.Header.Set("Authorization", "Bearer "+token)
			}
		}
		c.Next()
	}
}
