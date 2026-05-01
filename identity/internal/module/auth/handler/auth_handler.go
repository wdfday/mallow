package handler

import (
	"mallow/identity/internal/module/auth/dto"
	"mallow/identity/internal/module/auth/service"
	"net/http"

	"mallow/identity/internal/config"
	"mallow/identity/internal/middleware"
	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
)

// AuthHandler serves pure authentication (login/register/token) endpoints.
type AuthHandler struct {
	authService    service.IAuthService
	sessionService service.ISessionService
	config         *config.Config
}

// NewAuthHandler constructs an AuthHandler with required dependencies.
func NewAuthHandler(authService service.IAuthService, sessionService service.ISessionService, cfg *config.Config) *AuthHandler {
	return &AuthHandler{
		authService:    authService,
		sessionService: sessionService,
		config:         cfg,
	}
}

// RegisterRoutes registers authentication-related routes (login, register, token refresh)
func (h *AuthHandler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware) {
	// Public authentication routes
	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/register", h.register)
		auth.POST("/login", h.login)
		auth.POST("/google", h.authenticateGoogle)
		auth.POST("/refresh", h.refreshToken)
	}

	// Protected authentication routes
	protected := r.Group("/api/v1/auth")
	protected.Use(authMiddleware.AuthMiddleware())
	{
		protected.POST("/logout", h.logout)
	}
}

// Register godoc
// @Summary Register a new user
// @Description Register a new user account
// @Tags auth
// @Accept json
// @Produce json
// @Param user body dto.RegisterRequest true "User registration data"
// @Success 201 {object} dto.AuthResponse
// @Failure 400 {object} shared.ErrorResponse
// @Failure 409 {object} shared.ErrorResponse
// @Router /api/v1/auth/register [post]
func (h *AuthHandler) register(c *gin.Context) {
	var req dto.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	result, err := h.authService.Register(c.Request.Context(), req)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	if err := h.sessionService.CreateSession(c.Request.Context(), result.SessionID, result.User.ID.String(), c.ClientIP(), c.Request.UserAgent()); err != nil {
		shared.HandleError(c, err)
		return
	}

	// Set refresh token in HTTP-only cookie
	h.setRefreshTokenCookie(c, result.RefreshToken)

	// Return auth response without refresh token (it's in cookie)
	response := dto.NewAuthResponse(result.User, result.Profile, result.AccessToken, result.ExpiresAt)
	shared.RespondWithSuccess(c, http.StatusCreated, "User registered successfully", response)
}

// Login godoc
// @Summary User login
// @Description Authenticate user with email and password
// @Tags auth
// @Accept json
// @Produce json
// @Param credentials body dto.LoginRequest true "Login credentials"
// @Success 200 {object} dto.AuthResponse
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/auth/login [post]
func (h *AuthHandler) login(c *gin.Context) {
	var req dto.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	// Get client IP
	req.IP = c.ClientIP()

	result, err := h.authService.Login(c.Request.Context(), req)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	if err := h.sessionService.CreateSession(c.Request.Context(), result.SessionID, result.User.ID.String(), c.ClientIP(), c.Request.UserAgent()); err != nil {
		shared.HandleError(c, err)
		return
	}

	// Set refresh token in HTTP-only cookie
	h.setRefreshTokenCookie(c, result.RefreshToken)

	// Return auth response without refresh token (it's in cookie)
	response := dto.NewAuthResponse(result.User, result.Profile, result.AccessToken, result.ExpiresAt)
	shared.RespondWithSuccess(c, http.StatusOK, "Login successful", response)
}

// AuthenticateGoogle godoc
// @Summary Google OAuth authentication
// @Description Authenticate user with Google OAuth token
// @Tags auth
// @Accept json
// @Produce json
// @Param googleAuth body dto.GoogleAuthRequest true "Google OAuth token"
// @Success 200 {object} dto.AuthResponse
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/auth/google [post]
func (h *AuthHandler) authenticateGoogle(c *gin.Context) {
	var req dto.GoogleAuthRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	result, err := h.authService.AuthenticateGoogle(c.Request.Context(), req)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	if err := h.sessionService.CreateSession(c.Request.Context(), result.SessionID, result.User.ID.String(), c.ClientIP(), c.Request.UserAgent()); err != nil {
		shared.HandleError(c, err)
		return
	}

	// Set refresh token in HTTP-only cookie
	h.setRefreshTokenCookie(c, result.RefreshToken)

	// Return auth response without refresh token (it's in cookie)
	response := dto.NewAuthResponse(result.User, result.Profile, result.AccessToken, result.ExpiresAt)
	shared.RespondWithSuccess(c, http.StatusOK, "Google authentication successful", response)
}

// RefreshToken godoc
// @Summary Refresh access token
// @Description Generate a new access token using refresh token from HTTP-only cookie
// @Tags auth
// @Accept json
// @Produce json
// @Success 200 {object} dto.TokenResponse
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/auth/refresh [post]
func (h *AuthHandler) refreshToken(c *gin.Context) {
	// Get refresh token from cookie only
	refreshToken, err := h.getRefreshTokenFromCookie(c)
	if err != nil || refreshToken == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "refresh token not found in cookie")
		return
	}

	response, err := h.authService.RefreshToken(c.Request.Context(), refreshToken)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	// Refresh endpoint keeps the same refresh token but extends cookie lifetime
	h.setRefreshTokenCookie(c, refreshToken)

	shared.RespondWithSuccess(c, http.StatusOK, "Token refreshed successfully", response)
}

// Logout godoc
// @Summary User logout
// @Description Logout user by blacklisting their refresh token
// @Tags auth
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/auth/logout [post]
func (h *AuthHandler) logout(c *gin.Context) {
	// Get current user from context (set by auth middleware)
	user, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	// Try to get refresh token from cookie
	refreshToken, err := h.getRefreshTokenFromCookie(c)
	if err != nil || refreshToken == "" {
		// If not in cookie, user might have already logged out or cookie expired
		shared.RespondWithError(c, http.StatusBadRequest, "refresh token not found")
		return
	}

	// Get client IP
	ipAddress := c.ClientIP()

	// Call service to blacklist token
	if err := h.authService.Logout(c.Request.Context(), user.ID.String(), refreshToken, ipAddress); err != nil {
		shared.HandleError(c, err)
		return
	}

	// Clear the cookie
	h.clearRefreshTokenCookie(c)

	// Return success message
	shared.RespondWithSuccessNoData(c, http.StatusOK, "Logged out successfully")
}
