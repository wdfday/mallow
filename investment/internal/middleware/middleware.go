package middleware

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	pkgmw "mallow/pkg/middleware"
)

// AuthUser holds the authenticated user's identity extracted from context.
type AuthUser struct {
	ID    uuid.UUID
	Role  string
	Email string
}

const userKey = "current_user"

// GetCurrentUser retrieves the authenticated user from the gin context.
// jwtMiddleware (router.go) sets user_id, user_role, user_email from gateway headers.
func GetCurrentUser(c *gin.Context) (AuthUser, bool) {
	if v, ok := c.Get(userKey); ok {
		if u, ok := v.(AuthUser); ok {
			return u, true
		}
	}
	// Build from individual context keys set by pkgmw.TrustedHeaders.
	raw, exists := c.Get(pkgmw.CtxUserID)
	if !exists {
		slog.Warn("investment current user missing from context",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"reason", "missing_ctx_user_id",
		)
		return AuthUser{}, false
	}
	var idStr string
	switch v := raw.(type) {
	case string:
		idStr = v
	case uuid.UUID:
		idStr = v.String()
	default:
		slog.Warn("investment current user invalid context type",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"type", fmt.Sprintf("%T", raw),
		)
		return AuthUser{}, false
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		slog.Warn("investment current user invalid uuid",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"user_id", idStr,
		)
		return AuthUser{}, false
	}
	u := AuthUser{ID: id}
	if role, ok := c.Get(pkgmw.CtxUserRole); ok {
		u.Role, _ = role.(string)
	}
	if email, ok := c.Get(pkgmw.CtxUserEmail); ok {
		u.Email, _ = email.(string)
	}
	return u, true
}

// Middleware provides auth middleware for investment service handlers.
// The gateway sets X-User-ID; jwtMiddleware in router validates presence.
// This type exists for compatibility with moved broker/account handler signatures.
type Middleware struct{}

// New creates a Middleware instance.
func New() *Middleware { return &Middleware{} }

// AuthOptions configures authentication middleware behavior.
type AuthOptions struct {
	EmailVerified bool
}

// WithEmailVerified sets EmailVerified option.
func WithEmailVerified() func(*AuthOptions) {
	return func(opts *AuthOptions) { opts.EmailVerified = true }
}

// AuthMiddleware returns a gin.HandlerFunc that verifies user_id is set in context.
// The outer jwtMiddleware (router.go) already sets it; this is a safety guard.
func (m *Middleware) AuthMiddleware(opts ...func(*AuthOptions)) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, exists := c.Get(pkgmw.CtxUserID)
		if !exists {
			slog.Warn("investment auth rejected request",
				"path", c.Request.URL.Path,
				"method", c.Request.Method,
				"reason", "missing_ctx_user_id",
			)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		// Normalise to string if it came as uuid.UUID
		switch v := raw.(type) {
		case uuid.UUID:
			c.Set(pkgmw.CtxUserID, v.String())
		case string:
			if v == "" {
				slog.Warn("investment auth rejected request",
					"path", c.Request.URL.Path,
					"method", c.Request.Method,
					"reason", "empty_ctx_user_id",
				)
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
				return
			}
		}
		slog.Info("investment auth accepted request",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"user_id", pkgmw.UserID(c),
			"user_role", pkgmw.UserRole(c),
			"user_email", pkgmw.UserEmail(c),
		)
		c.Next()
	}
}
