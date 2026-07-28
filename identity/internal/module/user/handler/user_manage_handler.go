package handler

import (
	"net/http"
	"strings"

	"mallow/identity/internal/middleware"
	"mallow/identity/internal/module/user/domain"
	"mallow/identity/internal/module/user/dto"
	"mallow/identity/internal/module/user/service"
	"mallow/identity/internal/shared"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AdminHandler struct {
	service service.IUserService
}

func NewAdminHandler(service service.IUserService) *AdminHandler {
	return &AdminHandler{service: service}
}

func (h *AdminHandler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware) {
	admin := r.Group("/api/v1/user/manage")
	admin.Use(authMiddleware.AuthMiddleware(middleware.WithAdminOnly(), middleware.WithIsNotSuspended()))
	{
		admin.GET("", h.list)
		admin.PATCH("/:id/suspend", h.suspend)
		admin.PATCH("/:id/reinstate", h.reinstate)
		admin.PATCH("/:id/role", h.changeRole)
	}
}

// ListUsers godoc
// @Summary List user
// @Description Get a paginated list of user
// @Tags user
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param page query int false "Page number" default(1)
// @Param per_page query int false "Items per page" default(20)
// @Param query query string false "Search query (email, name)"
// @Param role query string false "Filter by role"
// @Param status query string false "Filter by status"
// @Success 200 {object} dto.UserListResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/user/manage [get]
func (h *AdminHandler) list(c *gin.Context) {
	var req dto.ListUsersRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		shared.HandleError(c, shared.ErrValidation.WithDetails("message", "invalid query parameters"))
		return
	}

	filter := domain.ListUsersFilter{
		Query:      strings.TrimSpace(req.Query),
		ActiveOnly: true,
	}

	if req.Role != "" {
		role := domain.UserRole(strings.ToLower(req.Role))
		if role != domain.UserRoleAdmin && role != domain.UserRoleUser {
			shared.HandleError(c, shared.ErrValidation.WithDetails("field", "role").WithDetails("message", "invalid role filter"))
			return
		}
		filter.Role = &role
	}

	if req.Status != "" {
		status := domain.UserStatus(strings.ToLower(req.Status))
		switch status {
		case domain.UserStatusActive, domain.UserStatusPendingVerification, domain.UserStatusSuspended:
			filter.Status = &status
		default:
			shared.HandleError(c, shared.ErrValidation.WithDetails("field", "status").WithDetails("message", "invalid status filter"))
			return
		}
	}

	pagination := shared.Pagination{
		Page:    req.Page,
		PerPage: req.PerPage,
		Sort:    req.Sort,
	}

	if pagination.Page <= 0 {
		pagination.Page = shared.DefaultPage
	}
	if pagination.PerPage <= 0 {
		pagination.PerPage = shared.DefaultPageSize
	}
	if pagination.PerPage > shared.MaxPageSize {
		pagination.PerPage = shared.MaxPageSize
	}
	if strings.TrimSpace(pagination.Sort) == "" {
		pagination.Sort = "last_active_at desc"
	}

	page, err := h.service.List(c.Request.Context(), filter, pagination)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Users retrieved successfully", dto.UsersPageToResponse(page))
}

// SuspendUser godoc
// @Summary Suspend a user
// @Description Suspend a user by ID
// @Tags user
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/user/manage/{id}/suspend [patch]
func (h *AdminHandler) suspend(c *gin.Context) {
	userID, ok := h.parseUserID(c)
	if !ok {
		return
	}

	updates := map[string]any{
		"status": string(domain.UserStatusSuspended),
	}

	if err := h.service.UpdateColumns(c.Request.Context(), userID, updates); err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccessNoData(c, http.StatusOK, "user suspended successfully")
}

// ReinstateUser godoc
// @Summary Reinstate a suspended user
// @Description Reinstate a suspended user by ID
// @Tags user
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/user/manage/{id}/reinstate [patch]
func (h *AdminHandler) reinstate(c *gin.Context) {
	userID, ok := h.parseUserID(c)
	if !ok {
		return
	}

	updates := map[string]any{
		"status":       string(domain.UserStatusActive),
		"locked_until": nil,
	}

	if err := h.service.UpdateColumns(c.Request.Context(), userID, updates); err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccessNoData(c, http.StatusOK, "user reinstated successfully")
}

// ChangeUserRole godoc
// @Summary Change a user's role
// @Description Change the role of a user by ID
// @Tags user
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "User ID"
// @Param role body dto.ChangeUserRoleRequest true "New Role"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/user/manage/{id}/role [patch]
func (h *AdminHandler) changeRole(c *gin.Context) {
	userID, ok := h.parseUserID(c)
	if !ok {
		return
	}

	var req dto.ChangeUserRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.HandleError(c, shared.ErrInvalidRequestBody)
		return
	}

	role := domain.UserRole(strings.ToLower(req.Role))
	if role != domain.UserRoleAdmin && role != domain.UserRoleUser {
		shared.HandleError(c, shared.ErrValidation.WithDetails("field", "role").WithDetails("message", "invalid role"))
		return
	}

	if err := h.service.UpdateColumns(c.Request.Context(), userID, map[string]any{"role": string(role)}); err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccessNoData(c, http.StatusOK, "user role updated successfully")
}

func (h *AdminHandler) parseUserID(c *gin.Context) (string, bool) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		shared.HandleError(c, shared.ErrBadRequest.WithDetails("param", "id").WithDetails("message", "user id is required"))
		return "", false
	}

	if _, err := uuid.Parse(id); err != nil {
		shared.HandleError(c, shared.ErrBadRequest.WithDetails("param", "id").WithDetails("message", "invalid user id"))
		return "", false
	}

	return id, true
}
