package handler

import (
	dto2 "mallow/identity/internal/module/auth/dto"
	"mallow/identity/internal/module/auth/service"
	"net/http"

	"mallow/identity/internal/middleware"
	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
)

// PasswordHandler serves password-related endpoints (change/forgot/reset).
type PasswordHandler struct {
	passwordService service.IPasswordService
}

// NewPasswordHandler constructs a PasswordHandler.
func NewPasswordHandler(passwordService service.IPasswordService) *PasswordHandler {
	return &PasswordHandler{passwordService: passwordService}
}

// RegisterRoutes registers password management routes (change, forgot, reset)
func (h *PasswordHandler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware, emailVerificationMiddleware *middleware.EmailVerificationMiddleware) {
	// Public password routes
	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/forgot-password", h.forgotPassword)
		auth.POST("/reset-password", h.resetPassword)
	}

	// Protected password routes (require authentication + email verification)
	protected := r.Group("/api/v1/auth")
	protected.Use(authMiddleware.AuthMiddleware())
	protected.Use(emailVerificationMiddleware.RequireVerifiedEmail())
	{
		protected.POST("/change-password", h.changePassword)
	}
}

// ChangePassword godoc
// @Summary Change password
// @Description Change the password of the currently authenticated user
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param password body dto2.ChangePasswordRequest true "Password change data"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/auth/change-password [post]
func (h *PasswordHandler) changePassword(c *gin.Context) {
	user, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	var req dto2.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	if err := h.passwordService.ChangePassword(c.Request.Context(), user.ID.String(), req); err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccessNoData(c, http.StatusOK, "Password changed successfully")
}

// ForgotPassword godoc
// @Summary Request password reset
// @Description Request a password reset link to be sent to email
// @Tags auth
// @Accept json
// @Produce json
// @Param forgot body dto2.ForgotPasswordRequest true "Email address"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Router /api/v1/auth/forgot-password [post]
func (h *PasswordHandler) forgotPassword(c *gin.Context) {
	var req dto2.ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	ipAddress := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")

	// Ignore errors intentionally - we always return success for security reasons
	// This prevents user enumeration attacks
	_ = h.passwordService.ForgotPassword(c.Request.Context(), req.Email, ipAddress, userAgent)

	// Always return success for security reasons
	shared.RespondWithSuccessNoData(c, http.StatusOK, "If the email exists, a password reset link has been sent")
}

// ResetPassword godoc
// @Summary Reset password
// @Description Reset password with reset token
// @Tags auth
// @Accept json
// @Produce json
// @Param reset body dto2.ResetPasswordRequest true "Reset token and new password"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/auth/reset-password [post]
func (h *PasswordHandler) resetPassword(c *gin.Context) {
	var req dto2.ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	if err := h.passwordService.ResetPassword(c.Request.Context(), req.Token, req.NewPassword); err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccessNoData(c, http.StatusOK, "Password reset successfully")
}
