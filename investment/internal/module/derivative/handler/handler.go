package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mallow/investment/internal/middleware"
	derivdomain "mallow/investment/internal/module/derivative/domain"
	"mallow/investment/internal/module/derivative/service"
	"mallow/investment/internal/shared"
)

type Handler struct {
	svc service.Service
}

func New(svc service.Service) *Handler {
	return &Handler{svc: svc}
}

// List godoc
// @Summary List derivative positions
// @Description Get derivative positions for the authenticated user
// @Tags portfolio
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param status query string false "Derivative position status" Enums(open, closed)
// @Success 200 {object} shared.SuccessResponse[[]derivdomain.DerivativePosition]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/derivatives [get]
func (h *Handler) List(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	status := c.DefaultQuery("status", "open")

	var positions []derivdomain.DerivativePosition
	positions, err := h.svc.List(c.Request.Context(), user.ID, status)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Derivative positions retrieved successfully", positions)
}
