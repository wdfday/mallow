package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mallow/investment/internal/middleware"
	positiondomain "mallow/investment/internal/module/position/domain"
	"mallow/investment/internal/module/position/service"
	"mallow/investment/internal/shared"
)

type Handler struct {
	svc service.Service
}

func New(svc service.Service) *Handler {
	return &Handler{svc: svc}
}

// List godoc
// @Summary List portfolio positions
// @Description Get the authenticated user's portfolio positions
// @Tags portfolio
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param status query string false "Position status" Enums(active, closed)
// @Success 200 {object} shared.SuccessResponse[[]positiondomain.PortfolioPosition]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/positions [get]
func (h *Handler) List(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	status := c.DefaultQuery("status", "active")

	var positions []positiondomain.PortfolioPosition
	positions, err := h.svc.List(c.Request.Context(), user.ID, status)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Positions retrieved successfully", positions)
}

// Get godoc
// @Summary Get portfolio position by symbol
// @Description Get a single portfolio position for the authenticated user
// @Tags portfolio
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param symbol path string true "Asset symbol"
// @Success 200 {object} shared.SuccessResponse[positiondomain.PortfolioPosition]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/investment/positions/{symbol} [get]
func (h *Handler) Get(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	symbol := c.Param("symbol")

	pos, err := h.svc.GetBySymbol(c.Request.Context(), user.ID, symbol)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "position not found")
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Position retrieved successfully", pos)
}
