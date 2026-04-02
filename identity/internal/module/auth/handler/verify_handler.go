package handler

import (
	dto2 "mallow/identity/internal/module/auth/dto"
	"mallow/identity/internal/module/auth/service"
	"net/http"

	"mallow/identity/internal/middleware"
	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
)

// VerifyHandler manages email verification routes.
type VerifyHandler struct {
	verificationService service.IVerificationService
}

// NewVerifyHandler constructs a VerifyHandler.
func NewVerifyHandler(verificationService service.IVerificationService) *VerifyHandler {
	return &VerifyHandler{verificationService: verificationService}
}

// RegisterRoutes registers email verification routes
func (h *VerifyHandler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware) {
	// Public verification routes
	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/verify-email", h.verifyEmail)
		auth.POST("/resend-verification", h.resendVerification)
	}

	// Protected verification routes
	protected := r.Group("/api/v1/auth")
	protected.Use(authMiddleware.AuthMiddleware())
	{
		protected.POST("/send-verification", h.sendVerification)
	}
}

// VerifyEmail godoc
// @Summary Verify email address
// @Description Verify user email with verification token
// @Tags auth
// @Accept json
// @Produce json
// @Param verification body dto2.VerifyEmailRequest true "Verification token"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/auth/verify-email [post]
func (h *VerifyHandler) verifyEmail(c *gin.Context) {
	var req dto2.VerifyEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	if err := h.verificationService.VerifyEmail(c.Request.Context(), req.Token); err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccessNoData(c, http.StatusOK, "Email verified successfully")
}

// SendVerification godoc
// @Summary Send verification email
// @Description Send verification email to current user
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} shared.Success
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/auth/send-verification [post]
func (h *VerifyHandler) sendVerification(c *gin.Context) {
	user, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	ipAddress := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")

	if err := h.verificationService.SendVerificationEmail(c.Request.Context(), user.ID.String(), ipAddress, userAgent); err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccessNoData(c, http.StatusOK, "Verification email sent successfully")
}

// ResendVerification godoc
// @Summary Resend verification email
// @Description Resend verification email to a user email address
// @Tags auth
// @Accept json
// @Produce json
// @Param email body dto2.ResendVerificationRequest true "Email address"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Router /api/v1/auth/resend-verification [post]
func (h *VerifyHandler) resendVerification(c *gin.Context) {
	var req dto2.ResendVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	ipAddress := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")

	// Resend verification email
	if err := h.verificationService.ResendVerificationEmail(c.Request.Context(), req.Email, ipAddress, userAgent); err != nil {
		// Don't reveal if email exists - always return success
		shared.RespondWithSuccessNoData(c, http.StatusOK, "If the email exists, a verification email has been sent")
		return
	}

	shared.RespondWithSuccessNoData(c, http.StatusOK, "Verification email sent successfully")
}
