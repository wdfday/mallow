// Package middleware provides shared Gin middleware for internal (behind-gateway) services.
// These helpers implement Pattern A: the API gateway validates the end-user JWT and injects
// X-User-ID / X-User-Role / X-User-Email headers; downstream services trust those headers.
package middleware

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	CtxUserID    = "user_id"
	CtxUserRole  = "user_role"
	CtxUserEmail = "user_email"
)

// TrustedHeaders reads gateway-injected user-context headers and stores them in the
// gin context so handlers can call UserID / UserRole / UserEmail helpers.
// Required: X-User-ID must be present; missing it returns 401.
func TrustedHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		uid := c.GetHeader("X-User-ID")
		if uid == "" {
			slog.Warn("trusted headers rejected request",
				"path", c.Request.URL.Path,
				"method", c.Request.Method,
				"reason", "missing_x_user_id",
				"x_user_role", c.GetHeader("X-User-Role"),
				"x_user_email", c.GetHeader("X-User-Email"),
			)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing X-User-ID"})
			return
		}
		c.Set(CtxUserID, uid)
		if role := c.GetHeader("X-User-Role"); role != "" {
			c.Set(CtxUserRole, role)
		}
		if email := c.GetHeader("X-User-Email"); email != "" {
			c.Set(CtxUserEmail, email)
		}
		slog.Info("trusted headers accepted request",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"x_user_id", uid,
			"x_user_role", c.GetHeader("X-User-Role"),
			"x_user_email", c.GetHeader("X-User-Email"),
		)
		c.Next()
	}
}

// TrustedHeadersOptional is like TrustedHeaders but does not abort when X-User-ID is absent.
// Use for routes that are accessible both authenticated and unauthenticated.
func TrustedHeadersOptional() gin.HandlerFunc {
	return func(c *gin.Context) {
		if uid := c.GetHeader("X-User-ID"); uid != "" {
			c.Set(CtxUserID, uid)
		}
		if role := c.GetHeader("X-User-Role"); role != "" {
			c.Set(CtxUserRole, role)
		}
		if email := c.GetHeader("X-User-Email"); email != "" {
			c.Set(CtxUserEmail, email)
		}
		slog.Info("trusted headers optional request",
			"path", c.Request.URL.Path,
			"method", c.Request.Method,
			"x_user_id", c.GetHeader("X-User-ID"),
			"x_user_role", c.GetHeader("X-User-Role"),
			"x_user_email", c.GetHeader("X-User-Email"),
		)
		c.Next()
	}
}

// UserID returns the authenticated user's ID from context, or "".
func UserID(c *gin.Context) string {
	v, _ := c.Get(CtxUserID)
	s, _ := v.(string)
	return s
}

// UserRole returns the authenticated user's role from context, or "".
func UserRole(c *gin.Context) string {
	v, _ := c.Get(CtxUserRole)
	s, _ := v.(string)
	return s
}

// UserEmail returns the authenticated user's email from context, or "".
func UserEmail(c *gin.Context) string {
	v, _ := c.Get(CtxUserEmail)
	s, _ := v.(string)
	return s
}
