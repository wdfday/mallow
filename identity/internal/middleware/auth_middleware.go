package middleware

import (
	"net/http"
	"strings"

	authDomain "mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/auth/service"
	userDomain "mallow/identity/internal/module/user/domain"
	userService "mallow/identity/internal/module/user/service"

	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	UserKey = "current_user"
)

// Middleware handles authentication middleware
type Middleware struct {
	jwtService service.IJWTService
	// userService is kept for potential future use (e.g., real-time status checks)
	// Currently, all checks are done from token claims for better performance
	userService userService.IUserService
}

// NewMiddleware creates a new auth middleware
func NewMiddleware(jwtService service.IJWTService, userService userService.IUserService) *Middleware {
	return &Middleware{
		jwtService:  jwtService,
		userService: userService, // Kept for backward compatibility and future use
	}
}

// AuthOptions configures authentication middleware behavior
type AuthOptions struct {
	AdminOnly      bool // Require admin role
	IsNotSuspended bool // Require user not be suspended
	EmailVerified  bool // Require email to be verified
}

// WithAdminOnly sets AdminOnly option
func WithAdminOnly() func(*AuthOptions) {
	return func(opts *AuthOptions) {
		opts.AdminOnly = true
	}
}

// WithIsNotSuspended sets IsNotSuspended option
func WithIsNotSuspended() func(*AuthOptions) {
	return func(opts *AuthOptions) {
		opts.IsNotSuspended = true
	}
}

// WithEmailVerified sets EmailVerified option
func WithEmailVerified() func(*AuthOptions) {
	return func(opts *AuthOptions) {
		opts.EmailVerified = true
	}
}

// AuthMiddleware validates JWT token and applies optional authorization checks
func (m *Middleware) AuthMiddleware(options ...func(*AuthOptions)) gin.HandlerFunc {
	// Parse options
	opts := &AuthOptions{}
	for _, opt := range options {
		opt(opts)
	}

	return func(c *gin.Context) {
		logger := GetLogger(c)

		// Log authentication attempt
		logger.Debug("Authentication attempt",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"client_ip", c.ClientIP(),
			"user_agent", c.GetHeader("User-Agent"),
		)

		// Get token from Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			logger.Warn("Authentication failed: missing authorization header",
				"path", c.Request.URL.Path,
				"client_ip", c.ClientIP(),
			)
			shared.RespondWithError(c, http.StatusUnauthorized, "authorization header required")
			c.Abort()
			return
		}

		// Extract token - accept both "Bearer token" and raw token formats
		tokenString := ""
		if strings.HasPrefix(authHeader, "Bearer ") {
			// Standard format: "Bearer <token>"
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		} else if strings.HasPrefix(authHeader, "bearer ") {
			// Case-insensitive: "bearer <token>"
			tokenString = strings.TrimPrefix(authHeader, "bearer ")
		} else {
			// Assume it's a raw token (for Swagger compatibility)
			tokenString = authHeader
			logger.Debug("Authorization header without Bearer prefix, treating as raw token",
				"path", c.Request.URL.Path,
			)
		}

		// Trim whitespace
		tokenString = strings.TrimSpace(tokenString)

		if tokenString == "" {
			logger.Warn("Authentication failed: empty token",
				"path", c.Request.URL.Path,
				"client_ip", c.ClientIP(),
			)
			shared.RespondWithError(c, http.StatusUnauthorized, "token required")
			c.Abort()
			return
		}

		// Validate token using JWT service
		claims, err := m.jwtService.ValidateToken(tokenString)
		if err != nil {
			logger.Warn("Authentication failed: invalid token",
				"error", err,
				"path", c.Request.URL.Path,
				"client_ip", c.ClientIP(),
			)
			shared.RespondWithError(c, http.StatusUnauthorized, "invalid or expired token")
			c.Abort()
			return
		}

		// Parse user ID
		userID, err := uuid.Parse(claims.UserID)
		if err != nil {
			logger.Error("Authentication failed: invalid user ID",
				"error", err,
				"user_id_string", claims.UserID,
				"path", c.Request.URL.Path,
				"client_ip", c.ClientIP(),
			)
			shared.RespondWithError(c, http.StatusUnauthorized, "invalid user ID format")
			c.Abort()
			return
		}

		// Create auth user for context
		authUser := authDomain.AuthUser{
			ID:       userID,
			Username: claims.Email, // Using email as username
		}

		// Set user in context
		c.Set(UserKey, authUser)
		c.Set("user_id", userID)
		c.Set("user_email", claims.Email)
		c.Set("user_role", string(claims.Role)) // Convert to string for context

		// Apply authorization checks if needed
		if opts.AdminOnly || opts.IsNotSuspended || opts.EmailVerified {
			// Get user from database for additional checks
			user, err := m.userService.GetByID(c.Request.Context(), userID.String())
			if err != nil {
				logger.Warn("Authorization check failed: user not found",
					"user_id", userID.String(),
					"error", err,
					"path", c.Request.URL.Path,
				)
				shared.RespondWithError(c, http.StatusUnauthorized, "user not found")
				c.Abort()
				return
			}

			// Check admin role
			if opts.AdminOnly {
				roleStr := strings.ToLower(string(claims.Role))
				if roleStr != string(userDomain.UserRoleAdmin) {
					logger.Warn("Admin access denied",
						"path", c.Request.URL.Path,
						"user_role", roleStr,
						"client_ip", c.ClientIP(),
					)
					shared.RespondWithError(c, http.StatusForbidden, "admin access required")
					c.Abort()
					return
				}
				logger.Debug("Admin access granted",
					"path", c.Request.URL.Path,
					"user_role", roleStr,
				)
			}

			// Check if user is not suspended
			if opts.IsNotSuspended {
				if user.IsSuspended() {
					logger.Warn("Access denied: account suspended",
						"user_id", userID.String(),
						"path", c.Request.URL.Path,
						"client_ip", c.ClientIP(),
					)
					shared.RespondWithError(c, http.StatusForbidden, "account is suspended")
					c.Abort()
					return
				}
			}

			// Check if email is verified
			if opts.EmailVerified {
				if !user.EmailVerified {
					logger.Warn("Access denied: email not verified",
						"user_id", userID.String(),
						"email", user.Email,
						"path", c.Request.URL.Path,
						"client_ip", c.ClientIP(),
					)
					shared.RespondWithAppError(c, shared.ErrForbidden.WithDetails(
						"message", "email verification required",
					).WithDetails(
						"hint", "please verify your email before accessing this resource",
					))
					c.Abort()
					return
				}
				logger.Debug("Email verification check passed",
					"user_id", userID.String(),
					"email", user.Email,
				)
			}
		}

		// Log successful authentication
		logger.Info("Authentication successful",
			"user_id", userID.String(),
			"email", claims.Email,
			"role", string(claims.Role),
			"path", c.Request.URL.Path,
			"client_ip", c.ClientIP(),
		)

		c.Next()
	}
}

// GetCurrentUser retrieves the current user from context
func GetCurrentUser(c *gin.Context) (authDomain.AuthUser, bool) {
	user, exists := c.Get(UserKey)
	if !exists {
		return authDomain.AuthUser{}, false
	}

	authUser, ok := user.(authDomain.AuthUser)
	if !ok {
		return authDomain.AuthUser{}, false
	}

	return authUser, true
}
