package handler

import (
	"mallow/identity/internal/middleware"
	"mallow/identity/internal/module/user/service"

	"github.com/gin-gonic/gin"
)

// Handler aggregates all user-related HTTP handlers.
type Handler struct {
	user  *UserHandler
	admin *AdminHandler
}

// NewHandler constructs the aggregate handler with shared dependencies.
func NewHandler(service service.IUserService) *Handler {
	return &Handler{
		user:  NewUserHandler(service),
		admin: NewAdminHandler(service),
	}
}

// RegisterRoutes wires both user-facing and admin management routes.
func (h *Handler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware) {
	h.user.RegisterRoutes(r, authMiddleware)
	h.admin.RegisterRoutes(r, authMiddleware)
}
