package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mallow/investment/internal/middleware"
	"mallow/investment/internal/module/portfolio/event"
	snapshotdomain "mallow/investment/internal/module/snapshot/domain"
	"mallow/investment/internal/module/snapshot/repository"
	"mallow/investment/internal/module/snapshot/service"
	"mallow/investment/internal/shared"
)

type Handler struct {
	svc service.Service
}

func New(svc service.Service) *Handler {
	return &Handler{svc: svc}
}

// List godoc
// @Summary List portfolio snapshots
// @Description Get portfolio snapshots for the authenticated user
// @Tags portfolio
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param snapshot_type query string false "Snapshot type" Enums(daily, weekly, monthly, manual)
// @Param type query string false "Alias for snapshot_type" Enums(daily, weekly, monthly, manual)
// @Success 200 {object} shared.SuccessResponse[[]snapshotdomain.PortfolioSnapshot]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/snapshots [get]
func (h *Handler) List(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	snapshotType := c.Query("snapshot_type")
	if snapshotType == "" {
		snapshotType = c.Query("type")
	}
	filter := repository.ListFilter{
		SnapshotType: snapshotType,
		Limit:        90,
	}

	var snaps []snapshotdomain.PortfolioSnapshot
	snaps, err := h.svc.List(c.Request.Context(), user.ID, filter)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Snapshots retrieved successfully", snaps)
}

func (h *Handler) Trigger(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req struct {
		AccountID    string `json:"account_id" binding:"required"`
		SnapshotType string `json:"snapshot_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.SnapshotType == "" {
		req.SnapshotType = "manual"
	}

	accountID, err := uuid.Parse(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid account_id"})
		return
	}

	snapType := event.SnapshotType(req.SnapshotType)
	if err := h.svc.Trigger(c.Request.Context(), accountID, user.ID, snapType); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "snapshot taken"})
}
