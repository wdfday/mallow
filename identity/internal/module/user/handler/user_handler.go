package handler

import (
	"net/http"
	"strings"

	"mallow/identity/internal/middleware"
	"mallow/identity/internal/module/user/dto"
	"mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
)

// UserHandler handles user-related HTTP requests
type UserHandler struct {
	service service.IUserService
}

// NewUserHandler creates a new UserHandler
func NewUserHandler(service service.IUserService) *UserHandler {
	return &UserHandler{service: service}
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
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	user, err := h.service.GetByID(c.Request.Context(), currentUser.ID.String())
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccess(c, http.StatusOK, "User profile retrieved successfully", dto.UserToProfileResponse(*user))
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
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	var req dto.UpdateUserProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	updates := make(map[string]any)

	if req.FullName != nil {
		fullName := strings.TrimSpace(*req.FullName)
		if fullName == "" {
			shared.RespondWithError(c, http.StatusBadRequest, "full_name cannot be empty")
			return
		}
		updates["full_name"] = fullName
	}

	if req.DisplayName != nil {
		displayName := strings.TrimSpace(*req.DisplayName)
		if displayName == "" {
			updates["display_name"] = nil
		} else {
			updates["display_name"] = displayName
		}
	}

	if req.PhoneNumber != nil {
		phone := strings.TrimSpace(*req.PhoneNumber)
		if phone == "" {
			updates["phone_number"] = nil
		} else {
			updates["phone_number"] = phone
		}
	}

	if len(updates) == 0 {
		shared.RespondWithError(c, http.StatusBadRequest, "no fields to update")
		return
	}

	if err := h.service.UpdateColumns(c.Request.Context(), currentUser.ID.String(), updates); err != nil {
		shared.HandleError(c, err)
		return
	}

	updatedUser, err := h.service.GetByID(c.Request.Context(), currentUser.ID.String())
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccess(c, http.StatusOK, "User profile updated successfully", dto.UserToProfileResponse(*updatedUser))
}
