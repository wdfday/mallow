package handler

import (
	"net/http"
	"strings"

	"mallow/identity/internal/middleware"
	profiledto "mallow/identity/internal/module/profile/dto"
	profileservice "mallow/identity/internal/module/profile/service"
	"mallow/identity/internal/module/user/dto"
	"mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
)

// UserHandler handles user-related HTTP requests
type UserHandler struct {
	service        service.IUserService
	profileService profileservice.Service
}

// NewUserHandler creates a new UserHandler
func NewUserHandler(service service.IUserService, profileService profileservice.Service) *UserHandler {
	return &UserHandler{service: service, profileService: profileService}
}

func (h *UserHandler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware) {
	user := r.Group("/api/v1/user")
	user.Use(authMiddleware.AuthMiddleware())
	{
		user.GET("/me", h.getMe)
		user.PUT("/me", h.updateMe)
	}
}

// GetMe godoc
// @Summary Get current user
// @Description Get details of the currently authenticated user
// @Tags user
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} dto.UserProfileResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/user/me [get]
func (h *UserHandler) getMe(c *gin.Context) {
	currentUser, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.HandleError(c, shared.ErrUserNotInContext)
		return
	}

	user, err := h.service.GetByID(c.Request.Context(), currentUser.ID.String())
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	// Fetch profile (nil-safe: legacy users may not have one)
	profile, _ := h.profileService.GetProfile(c.Request.Context(), user.ID.String())

	shared.RespondWithSuccess(c, http.StatusOK, "User profile retrieved successfully", dto.UserToProfileResponse(*user, profile))
}

// UpdateMe godoc
// @Summary Update current user
// @Description Update details of the currently authenticated user
// @Tags user
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param user body dto.UpdateUserProfileRequest true "User data"
// @Success 200 {object} dto.UserProfileResponse
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/user/me [put]
func (h *UserHandler) updateMe(c *gin.Context) {
	currentUser, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.HandleError(c, shared.ErrUserNotInContext)
		return
	}

	var req dto.UpdateUserProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.HandleError(c, shared.ErrInvalidRequestBody)
		return
	}

	hasAnyField := req.FullName != nil || req.DisplayName != nil || req.PhoneNumber != nil
	if !hasAnyField {
		shared.HandleError(c, shared.ErrBadRequest.WithDetails("message", "no fields to update"))
		return
	}

	// Update personal info (FullName, DisplayName) via profile service
	if req.FullName != nil || req.DisplayName != nil {
		var fullName *string
		var displayName *string

		if req.FullName != nil {
			trimmed := strings.TrimSpace(*req.FullName)
			if trimmed == "" {
				shared.HandleError(c, shared.ErrValidation.WithDetails("field", "full_name").WithDetails("message", "full_name cannot be empty"))
				return
			}
			fullName = &trimmed
		}
		if req.DisplayName != nil {
			displayName = req.DisplayName
		}

		profileReq := profiledto.UpdateProfileRequest{
			FullName:    fullName,
			DisplayName: displayName,
		}
		if _, err := h.profileService.UpdateProfile(c.Request.Context(), currentUser.ID.String(), profileReq); err != nil {
			shared.HandleError(c, err)
			return
		}
	}

	// Update auth/security fields (PhoneNumber) on users table
	if req.PhoneNumber != nil {
		userUpdates := make(map[string]any)
		phone := strings.TrimSpace(*req.PhoneNumber)
		if phone == "" {
			userUpdates["phone_number"] = nil
		} else {
			userUpdates["phone_number"] = phone
		}
		if err := h.service.UpdateColumns(c.Request.Context(), currentUser.ID.String(), userUpdates); err != nil {
			shared.HandleError(c, err)
			return
		}
	}

	updatedUser, err := h.service.GetByID(c.Request.Context(), currentUser.ID.String())
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	profile, _ := h.profileService.GetProfile(c.Request.Context(), updatedUser.ID.String())

	shared.RespondWithSuccess(c, http.StatusOK, "User profile updated successfully", dto.UserToProfileResponse(*updatedUser, profile))
}
