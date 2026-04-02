package replay

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mallow/investment/internal/module/portfolio/replay/dto"
	"mallow/investment/internal/shared"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Rebuild godoc
// @Summary Rebuild projections
// @Description Rebuild read-model projections for a single investment account
// @Tags admin
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.RebuildRequest true "Rebuild request"
// @Success 200 {object} shared.SuccessResponse[dto.RebuildResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/admin/rebuild [post]
func (h *Handler) Rebuild(c *gin.Context) {
	var req dto.RebuildRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.AccountID == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "account_id is required")
		return
	}

	accountID, err := uuid.Parse(req.AccountID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid account_id")
		return
	}

	n, err := h.svc.Rebuild(c.Request.Context(), accountID)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Projections rebuilt successfully", dto.RebuildResponse{
		AccountID:      accountID.String(),
		EventsReplayed: n,
	})
}
