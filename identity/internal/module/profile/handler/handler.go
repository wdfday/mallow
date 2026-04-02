package handler

import (
	"net/http"

	"mallow/identity/internal/middleware"
	profiledto "mallow/identity/internal/module/profile/dto"
	profileservice "mallow/identity/internal/module/profile/service"
	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
)

// Handler manages authenticated profile endpoints.
type Handler struct {
	service profileservice.Service
}

// NewHandler constructs a profile handler.
func NewHandler(service profileservice.Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes wires profile routes under /api/v1/profile.
func (h *Handler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware) {
	profile := r.Group("/api/v1/profile")
	profile.Use(authMiddleware.AuthMiddleware())
	{
		//profile.POST("/me", h.createProfile) // will create on user registration
		profile.GET("/me", h.getProfile)
		profile.PUT("/me", h.updateProfile)
	}
}

//// createProfile godoc
//// @Summary Create my profile
//// @Description Create a profile record for the authenticated user
//// @Tags profile
//// @Accept json
//// @Produce json
//// @Security BearerAuth
//// @Param profile body profiledto.CreateProfileRequest true "Profile data"
//// @Success 201 {object} profiledto.ProfileResponse
//// @Failure 400 {object} shared.ErrorResponse
//// @Failure 401 {object} shared.ErrorResponse
//// @Failure 409 {object} shared.ErrorResponse
//// @Failure 500 {object} shared.ErrorResponse
//// @Router /api/v1/profile/me [post]
//func (h *Handler) createProfile(c *gin.Context) {
//	currentUser, exists := middleware.GetCurrentUser(c)
//	if !exists {
//		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
//		return
//	}
//
//	var req profiledto.CreateProfileRequest
//	if err := c.ShouldBindJSON(&req); err != nil {
//		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
//		return
//	}
//
//	profile, err := h.service.CreateProfile(c.Request.Context(), currentUser.ID.String(), req)
//	if err != nil {
//		shared.HandleError(c, err)
//		return
//	}
//
//	shared.RespondWithSuccess(c, http.StatusCreated, "Profile created successfully", profiledto.ToProfileResponse(profile))
//}

// getProfile godoc
// @Summary Get my profile
// @Description Retrieve the authenticated user's profile
// @Tags profile
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} profiledto.ProfileResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/profile/me [get]
func (h *Handler) getProfile(c *gin.Context) {
	currentUser, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	profile, err := h.service.GetProfile(c.Request.Context(), currentUser.ID.String())
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Profile retrieved successfully", profiledto.ToProfileResponse(profile))
}

// updateProfile godoc
// @Summary Update my profile
// @Description Update profile details of the authenticated user
// @Tags profile
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param profile body profiledto.UpdateProfileRequest true "Profile data"
// @Success 200 {object} profiledto.ProfileResponse
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/profile/me [put]
func (h *Handler) updateProfile(c *gin.Context) {
	currentUser, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	var req profiledto.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid request data")
		return
	}

	profile, err := h.service.UpdateProfile(c.Request.Context(), currentUser.ID.String(), req)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Profile updated successfully", profiledto.ToProfileResponse(profile))
}
