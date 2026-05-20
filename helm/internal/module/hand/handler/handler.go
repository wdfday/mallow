package handler

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/hand/domain"
	dto "mallow/helm/internal/module/hand/dto"
	helmDto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

type Handler struct {
	handMgr HandService
	helmSvc HelmService
	reg     RuntimeRegistry
}

func New(handMgr HandService, helmSvc HelmService, reg RuntimeRegistry) *Handler {
	return &Handler{handMgr: handMgr, helmSvc: helmSvc, reg: reg}
}

func callerUserID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(pkgmw.UserID(c))
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, "invalid user")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) resolveHelmID(userID uuid.UUID, accountID, helmID uuid.UUID) (uuid.UUID, error) {
	if accountID != uuid.Nil {
		helm, err := h.helmSvc.GetByAccount(accountID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("helm not found")
		}
		if helm.UserID != userID {
			return uuid.Nil, fmt.Errorf("helm not found")
		}
		return helm.ID, nil
	}
	if helmID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("account_id or helm_id is required")
	}
	if err := h.helmSvc.CheckOwner(helmID, userID); err != nil {
		return uuid.Nil, fmt.Errorf("helm not found")
	}
	return helmID, nil
}

// checkHandOwner verifies that the hand's helm belongs to userID.
func (h *Handler) checkHandOwner(handIDStr string, userID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	handID, err := uuid.Parse(handIDStr)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid id")
	}
	bi, err := h.handMgr.Get(handID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if err := h.helmSvc.CheckOwner(bi.Data.HelmID, userID); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("not found")
	}
	return handID, bi.Data.HelmID, nil
}

func (h *Handler) Register(rg *gin.RouterGroup) {
	b := rg.Group("/hands")
	{
		b.POST("", h.create)
		b.GET("", h.list)
		b.GET("/:id", h.get)
		b.PUT("/:id", h.update)
		b.DELETE("/:id", h.delete)
		b.GET("/:id/activity", h.activity)
		b.GET("/:id/trades", h.trades)
		b.POST("/:id/start", h.start)
		b.POST("/:id/stop", h.stop)
		b.POST("/:id/restart", h.restart)
		b.POST("/:id/pause", h.pause)
		b.POST("/:id/resume", h.resume)
		b.POST("/:id/kill", h.kill)
		b.POST("/:id/release", h.release)
		b.POST("/:id/allocate-capital", h.allocateCapital)
	}

}

// CapitalSuggestion is one hand that can free up capital for a new hand.
type CapitalSuggestion struct {
	HandID          string          `json:"hand_id"`
	Name            string          `json:"name"`
	Allocated       decimal.Decimal `json:"allocated"`
	Deployed        decimal.Decimal `json:"deployed"`
	ReducibleBy     decimal.Decimal `json:"reducible_by"`     // max freeable = allocated - deployed
	SuggestedTarget decimal.Decimal `json:"suggested_target"` // allocated - min(reducibleBy, shortage)
}

// CapitalOverflow is returned (as JSON body) when a new or updated hand would
// exceed the helm's available capital. It carries actionable suggestions so the
// caller knows exactly which hands can be reduced and by how much.
type CapitalOverflow struct {
	Error       string              `json:"error"`
	HelmEquity  float64             `json:"helm_equity"`
	TotalAlloc  float64             `json:"total_allocated"`
	Requested   float64             `json:"requested"`
	Available   float64             `json:"available"`
	Suggestions []CapitalSuggestion `json:"suggestions"`
}

// checkCapitalAllocation validates that adding newPos to a Helm doesn't exceed
// the available capital (live portfolio equity). excludeHandID is the hand being
// updated (skip its existing allocation); pass "" when creating a new hand.
//
// Returns (*CapitalOverflow, nil) when capital is insufficient — the caller should
// respond with 422 and the overflow payload so the client can act on suggestions.
// Returns (nil, nil) when allocation is valid.
func checkCapitalAllocation(
	totalCapital float64,
	existing []domain.HandSummary,
	allocatedCapital decimal.Decimal,
	excludeHandID string,
) (*CapitalOverflow, error) {
	// ── Fixed-USD allocation check ──────────────────────────────────────────
	if allocatedCapital.IsPositive() {
		var used float64
		for _, b := range existing {
			if b.ID.String() == excludeHandID {
				continue
			}
			used += b.AllocatedCapital.InexactFloat64()
		}
		available := totalCapital - used
		requesting := allocatedCapital.InexactFloat64()
		if requesting > available {
			shortage := requesting - available
			return buildOverflow(
				fmt.Sprintf("insufficient capital: requesting %.2f but only %.2f available", requesting, available),
				totalCapital, used, requesting, available, shortage, existing, excludeHandID,
			), nil
		}
	}

	return nil, nil
}

// buildOverflow assembles a CapitalOverflow with sorted suggestions.
// Hands with the most reducible free capital are listed first.
func buildOverflow(
	msg string,
	helmEquity, usedAbs, requesting, available, shortage float64,
	existing []domain.HandSummary,
	excludeHandID string,
) *CapitalOverflow {
	var suggestions []CapitalSuggestion
	remaining := shortage
	for _, b := range existing {
		if b.ID.String() == excludeHandID {
			continue
		}
		alloc := b.AllocatedCapital
		if !alloc.IsPositive() {
			continue
		}
		deployed := b.DeployedCapital
		reducible := alloc.Sub(deployed)
		if !reducible.IsPositive() {
			continue
		}
		reduceBy := reducible
		if remaining > 0 {
			cap := decimal.NewFromFloat(remaining)
			if reducible.LessThan(cap) {
				reduceBy = reducible
			} else {
				reduceBy = cap
			}
			remaining -= reduceBy.InexactFloat64()
		}
		suggestions = append(suggestions, CapitalSuggestion{
			HandID:          b.ID.String(),
			Name:            b.Name,
			Allocated:       alloc,
			Deployed:        deployed,
			ReducibleBy:     reducible,
			SuggestedTarget: alloc.Sub(reduceBy),
		})
	}
	return &CapitalOverflow{
		Error:       msg,
		HelmEquity:  helmEquity,
		TotalAlloc:  usedAbs,
		Requested:   requesting,
		Available:   available,
		Suggestions: suggestions,
	}
}

// create godoc
// @Summary Create hand
// @Tags hands
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body dto.CreateHandReq true "Hand create request"
// @Success 201 {object} shared.SuccessResponse[domain.HandSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 403 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands [post]
func (h *Handler) create(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	var req dto.CreateHandReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := req.Strategy.Validate(); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	helmID, err := h.resolveHelmID(userID, req.AccountID, req.HelmID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	req.HelmID = helmID
	cfg := req.ToDomain()
	helm, err := h.helmSvc.Get(cfg.HelmID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "helm not found")
		return
	}
	if helm.UserID != userID {
		shared.RespondWithError(c, http.StatusNotFound, "helm not found")
		return
	}
	rt, err := h.reg.Get(helmID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "helm runtime not available")
		return
	}
	if overflow, _ := checkCapitalAllocation(rt.Portfolio.Summary().Equity.InexactFloat64(), h.handMgr.ListByHelm(helmID), cfg.AllocatedCapital, ""); overflow != nil {
		c.JSON(http.StatusUnprocessableEntity, overflow)
		return
	}
	instance, err := h.handMgr.Create(cfg)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusCreated, "Hand created successfully", instance.Summary())
}

// list godoc
// @Summary List hands
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helm_id query string false "Filter by helm ID"
// @Param account_id query string false "Filter by account ID"
// @Success 200 {object} shared.SuccessResponse[[]domain.HandSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/hands [get]
func (h *Handler) list(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	if helmStr := c.Query("helm_id"); helmStr != "" {
		helmID, err := uuid.Parse(helmStr)
		if err != nil {
			shared.RespondWithError(c, http.StatusBadRequest, "invalid helm_id")
			return
		}
		if err := h.helmSvc.CheckOwner(helmID, userID); err != nil {
			shared.RespondWithError(c, http.StatusNotFound, "helm not found")
			return
		}
		shared.RespondWithSuccess(c, http.StatusOK, "Hands retrieved successfully", h.handMgr.ListByHelm(helmID))
		return
	}
	if accountStr := c.Query("account_id"); accountStr != "" {
		accountID, err := uuid.Parse(accountStr)
		if err != nil {
			shared.RespondWithError(c, http.StatusBadRequest, "invalid account_id")
			return
		}
		helm, err := h.helmSvc.GetByAccount(accountID)
		if err != nil || helm.UserID != userID {
			shared.RespondWithError(c, http.StatusNotFound, "helm not found")
			return
		}
		shared.RespondWithSuccess(c, http.StatusOK, "Hands retrieved successfully", h.handMgr.ListByHelm(helm.ID))
		return
	}
	// Without helmestrator_id filter, list all hands the user can access.
	helms, err := h.helmSvc.ListByUser(userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	var hands []domain.HandSummary
	for _, o := range helms {
		hands = append(hands, h.handMgr.ListByHelm(o.ID)...)
	}
	if hands == nil {
		hands = []domain.HandSummary{}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hands retrieved successfully", hands)
}

// get godoc
// @Summary Get hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[domain.HandSummary]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id} [get]
func (h *Handler) get(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	bi, err := h.handMgr.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand retrieved successfully", bi.Summary())
}

// update godoc
// @Summary Update hand
// @Tags hands
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Hand ID"
// @Param request body dto.UpdateHandReq true "Hand update request"
// @Success 200 {object} shared.SuccessResponse[domain.HandSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id} [put]
func (h *Handler) update(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, helmID, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}

	var req dto.UpdateHandReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	patch := req.ToDomain()

	// Validate capital allocation when sizing changes.
	if req.AllocatedCapital > 0 {
		rt, err := h.reg.Get(helmID)
		if err != nil {
			shared.RespondWithError(c, http.StatusBadRequest, "helm runtime not available")
			return
		}
		if overflow, _ := checkCapitalAllocation(rt.Portfolio.Summary().Equity.InexactFloat64(), h.handMgr.ListByHelm(helmID), patch.AllocatedCapital, id.String()); overflow != nil {
			c.JSON(http.StatusUnprocessableEntity, overflow)
			return
		}
	}

	if err := h.handMgr.Update(id, patch); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	bi, _ := h.handMgr.Get(id)
	shared.RespondWithSuccess(c, http.StatusOK, "Hand updated successfully", bi.Summary())
}

// delete godoc
// @Summary Delete hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 403 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id} [delete]
func (h *Handler) delete(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, helmID, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if _, err := h.helmSvc.Get(helmID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "helm not found")
		return
	}
	if err := h.handMgr.Delete(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand deleted successfully", dto.HandActionResp{Status: "deleted", ID: id.String()})
}

// start godoc
// @Summary Start hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/start [post]
func (h *Handler) start(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.handMgr.Start(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand started successfully", dto.HandActionResp{Status: "started", ID: id.String()})
}

// stop godoc
// @Summary Stop hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/stop [post]
func (h *Handler) stop(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.handMgr.Stop(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand stopped successfully", dto.HandActionResp{Status: "stopped", ID: id.String()})
}

// restart godoc
// @Summary Restart hand — stop then start (re-registers with signal herald)
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/restart [post]
func (h *Handler) restart(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.handMgr.Restart(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand restarted successfully", dto.HandActionResp{Status: "running", ID: id.String()})
}

// pause godoc
// @Summary Pause hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/pause [post]
func (h *Handler) pause(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.handMgr.Pause(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand paused successfully", dto.HandActionResp{Status: "paused", ID: id.String()})
}

// resume godoc
// @Summary Resume hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/resume [post]
func (h *Handler) resume(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.handMgr.Resume(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand resumed successfully", dto.HandActionResp{Status: "running", ID: id.String()})
}

// kill godoc
// @Summary Kill hand — stop and flatten all positions
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/kill [post]
func (h *Handler) kill(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.handMgr.Kill(c.Request.Context(), id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand killed successfully", dto.HandActionResp{Status: "stopped", ID: id.String()})
}

// release godoc
// @Summary Release a hand (orphan open positions)
// @Description Stop the hand without closing positions. Open legs are marked orphaned in the poslog; the hand will not reclaim them on restart.
// @Tags hands
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/release [post]
func (h *Handler) release(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.handMgr.Release(c.Request.Context(), id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand released successfully", dto.HandActionResp{Status: "stopped", ID: id.String()})
}

// Metrics writes Prometheus-style text metrics for all runtimes.
// Metrics godoc
// @Summary Prometheus metrics
// @Tags system
// @Produce plain
// @Success 200 {string} string
// @Router /metrics [get]
// activity godoc
// @Summary Get hand activity log
// @Description Events from the HELM_EVENTS JetStream stream filtered by hand_id. Currently returns empty; JetStream query endpoint TBD.
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[[]runtime.ActivityEntry]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/activity [get]
func (h *Handler) activity(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	_, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	// TODO: query HELM_EVENTS JetStream, filter by hand_id, page by ?after=&limit=
	shared.RespondWithSuccess(c, http.StatusOK, "Activity retrieved", []runtime.ActivityEntry{})
}

// trades godoc
// @Summary List closed trades for a hand (poslog-backed, cursor paging)
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Param cursor query int false "Cursor from previous page (0 = start)" default(0)
// @Param limit query int false "Page size" default(100)
// @Success 200 {object} shared.SuccessResponse[helmDto.TradesPageResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/trades [get]
func (h *Handler) trades(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	bi, err := h.handMgr.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, err.Error())
		return
	}
	rt, err := h.reg.Get(bi.Data.HelmID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "helm runtime not active")
		return
	}

	cursor, _ := strconv.ParseUint(c.DefaultQuery("cursor", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	if limit < 1 || limit > 500 {
		limit = 100
	}

	if rt.PosLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "poslog unavailable")
		return
	}
	page, posErr := rt.PosLog.TradesPaged(c.Request.Context(), bi.Data.HelmID.String(), id.String(), cursor, limit)
	if posErr != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, posErr.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Trades retrieved", helmDto.PoslogPageToResp(page, limit))
}

func (h *Handler) Metrics(c *gin.Context) {
	runtimes := h.reg.All()
	c.Header("Content-Type", "text/plain; version=0.0.4")

	var totalEquity, totalCash decimal.Decimal
	for _, rt := range runtimes {
		s := rt.Portfolio.Summary()
		totalEquity = totalEquity.Add(s.Equity)
		totalCash = totalCash.Add(s.Cash)
	}

	out := formatMetric("helm_total_equity", totalEquity.InexactFloat64()) +
		formatMetric("helm_total_cash", totalCash.InexactFloat64()) +
		formatMetric("helm_running_hands", float64(len(h.handMgr.RunningHands()))) +
		formatMetric("helm_active_runtimes", float64(len(runtimes)))
	c.String(http.StatusOK, out)
}

func formatMetric(name string, val float64) string {
	return name + " " + formatFloat(val) + "\n"
}

func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%g", f)
}

// allocateCapital godoc
// @Summary Allocate capital
// @Tags hands
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Hand ID"
// @Param request body dto.AllocateCapitalReq true "Capital allocation request"
// @Success 200 {object} shared.SuccessResponse[domain.HandSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 422 {object} shared.CapitalOverflow
// @Router /api/v1/hands/{id}/allocate-capital [post]
func (h *Handler) allocateCapital(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, helmID, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}

	var req dto.AllocateCapitalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	bi, err := h.handMgr.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, err.Error())
		return
	}

	delta := decimal.NewFromFloat(req.Amount)
	newCapital := bi.Data.AllocatedCapital.Add(delta)

	if !newCapital.IsPositive() {
		shared.RespondWithError(c, http.StatusBadRequest, "new allocated capital must be greater than zero")
		return
	}

	// Validate capital allocation against helm budget
	rt, err := h.reg.Get(helmID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "helm runtime not available")
		return
	}

	if overflow, _ := checkCapitalAllocation(rt.Portfolio.Summary().Equity.InexactFloat64(), h.handMgr.ListByHelm(helmID), newCapital, id.String()); overflow != nil {
		c.JSON(http.StatusUnprocessableEntity, overflow)
		return
	}

	_, err = h.handMgr.AllocateCapital(id, delta)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Fetch updated details
	bi, _ = h.handMgr.Get(id)
	shared.RespondWithSuccess(c, http.StatusOK, "Capital allocated successfully", bi.Summary())
}
