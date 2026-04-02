package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mallow/investment/internal/middleware"
	cashdomain "mallow/investment/internal/module/cash_flow/domain"
	"mallow/investment/internal/module/cash_flow/repository"
	"mallow/investment/internal/module/cash_flow/service"
	"mallow/investment/internal/shared"
)

type Handler struct {
	svc service.Service
}

func New(svc service.Service) *Handler {
	return &Handler{svc: svc}
}

// List godoc
// @Summary List portfolio cash flows
// @Description Get cash flows for the authenticated user
// @Tags portfolio
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param flow_type query string false "Cash flow type"
// @Param type query string false "Alias for flow_type"
// @Success 200 {object} shared.SuccessResponse[[]cashdomain.PortfolioCashFlow]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/cash-flows [get]
func (h *Handler) List(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	flowType := c.Query("flow_type")
	if flowType == "" {
		flowType = c.Query("type")
	}
	filter := repository.ListFilter{
		FlowType: flowType,
		Limit:    50,
	}

	var flows []cashdomain.PortfolioCashFlow
	flows, err := h.svc.List(c.Request.Context(), user.ID, filter)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Cash flows retrieved successfully", flows)
}
