package handler

import (
	"net/http"
	"strconv"

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
// @Param limit query int false "Max records to return (default 90)"
// @Param offset query int false "Records to skip (default 0)"
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
	limit, offset := parsePagination(c, 90)
	filter := repository.ListFilter{
		SnapshotType: snapshotType,
		Limit:        limit,
		Offset:       offset,
	}

	var snaps []snapshotdomain.PortfolioSnapshot
	snaps, err := h.svc.List(c.Request.Context(), user.ID, filter)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Snapshots retrieved successfully", snaps)
}

// Trigger godoc
// @Summary Trigger a manual portfolio snapshot
// @Description Take an immediate portfolio snapshot for a given account
// @Tags portfolio
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 202 {object} shared.SuccessResponse[any]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/snapshots [post]
func (h *Handler) Trigger(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		AccountID    string `json:"account_id" binding:"required"`
		SnapshotType string `json:"snapshot_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	if req.SnapshotType == "" {
		req.SnapshotType = "manual"
	}

	accountID, err := uuid.Parse(req.AccountID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid account_id")
		return
	}

	snapType := event.SnapshotType(req.SnapshotType)
	if err := h.svc.Trigger(c.Request.Context(), accountID, user.ID, snapType); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccessNoData(c, http.StatusAccepted, "Snapshot triggered successfully")
}

// parsePagination reads limit/offset query params with sensible defaults.
func parsePagination(c *gin.Context, defaultLimit int) (limit, offset int) {
	limit = defaultLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return
}
