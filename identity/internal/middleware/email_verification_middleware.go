package middleware

import (
	"mallow/identity/internal/module/user/service"
	"net/http"

	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
)

// EmailVerificationMiddleware checks if user's email is verified
// This middleware should be applied after AuthMiddleware
type EmailVerificationMiddleware struct {
	userService service.IUserService
}

// NewEmailVerificationMiddleware creates a new email verification middleware
func NewEmailVerificationMiddleware(userService service.IUserService) *EmailVerificationMiddleware {
	return &EmailVerificationMiddleware{
		userService: userService,
	}
}

// RequireVerifiedEmail ensures the user has verified their email
func (m *EmailVerificationMiddleware) RequireVerifiedEmail() gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := GetLogger(c)

		// Get user ID from context (set by AuthMiddleware)
		userIDInterface, exists := c.Get("user_id")
		if !exists {
			logger.Warn("Email verification check failed: user not in context",
				"path", c.Request.URL.Path,
				"client_ip", c.ClientIP(),
			)
			shared.RespondWithError(c, http.StatusUnauthorized, "user not authenticated")
			c.Abort()
			return
		}

		userID, ok := userIDInterface.(string)
		if !ok {
			// Try UUID type
			if uuid, ok := userIDInterface.(interface{ String() string }); ok {
				userID = uuid.String()
			} else {
				logger.Error("Email verification check failed: invalid user ID type",
					"path", c.Request.URL.Path,
					"client_ip", c.ClientIP(),
				)
				shared.RespondWithError(c, http.StatusInternalServerError, "invalid user ID format in context")
				c.Abort()
				return
			}
		}

		logger.Debug("Checking email verification status",
			"user_id", userID,
			"path", c.Request.URL.Path,
		)

		// Get user from database to check verification status
		user, err := m.userService.GetByID(c.Request.Context(), userID)
		if err != nil {
			logger.Warn("Email verification check failed: user not found",
				"user_id", userID,
				"error", err,
				"path", c.Request.URL.Path,
				"client_ip", c.ClientIP(),
			)
			shared.RespondWithAppError(c, shared.ErrUnauthorized.WithDetails("message", "user not found"))
			c.Abort()
			return
		}

		// Check if email is verified
		if !user.EmailVerified {
			logger.Warn("Email verification required",
				"user_id", userID,
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
			"user_id", userID,
			"email", user.Email,
		)

		// Email is verified, proceed to next handler
		c.Next()
	}
}
