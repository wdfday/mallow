package handler

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/eventlog"
	"mallow/helm/internal/module/hand/domain"
	dto "mallow/helm/internal/module/hand/dto"
	helmDto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

type Handler struct {
	handMgr  HandService
	helmSvc  HelmService
	reg      RuntimeRegistry
	eventLog eventlog.Log // nil = eventlog not available (dev/test without DB)
}

func New(handMgr HandService, helmSvc HelmService, reg RuntimeRegistry, evLog eventlog.Log) *Handler {
	return &Handler{handMgr: handMgr, helmSvc: helmSvc, reg: reg, eventLog: evLog}
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
// Works for both active (in-memory) and terminal (DB-only) hands.
func (h *Handler) checkHandOwner(handIDStr string, userID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	handID, err := uuid.Parse(handIDStr)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("invalid id")
	}
	summary, err := h.handMgr.GetSummary(handID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if err := h.helmSvc.CheckOwner(summary.HelmID, userID); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("not found")
	}
	return handID, summary.HelmID, nil
}

func (h *Handler) Register(rg *gin.RouterGroup) {
	b := rg.Group("/hands")
	{
		b.POST("", h.create)
		b.GET("", h.list)
		b.GET("/:id", h.get)
		b.PUT("/:id", h.update)
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
	handID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	summary, err := h.handMgr.GetSummary(handID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.helmSvc.CheckOwner(summary.HelmID, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand retrieved successfully", summary)
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
// @Description Persistent events for this hand from PostgreSQL, newest first. Supports ?limit= (default 100) and ?before= (RFC3339 cursor for pagination).
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Param limit query int false "Max events to return" default(100)
// @Param before query string false "Return events before this RFC3339 timestamp (pagination cursor)"
// @Success 200 {object} shared.SuccessResponse[[]eventlog.Event]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/hands/{id}/activity [get]
func (h *Handler) activity(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	handID, helmID, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if h.eventLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "activity log not available")
		return
	}

	limit, _ := strconv.Atoi(c.Query("limit"))
	f := eventlog.Filter{
		HelmID: helmID,
		HandID: &handID,
		Limit:  limit,
	}
	if beforeStr := c.Query("before"); beforeStr != "" {
		if t, err := time.Parse(time.RFC3339, beforeStr); err == nil {
			f.Before = t
		}
	}

	events, err := h.eventLog.Query(c.Request.Context(), f)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, "failed to query activity log")
		return
	}
	if events == nil {
		events = []eventlog.Event{}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Activity retrieved", events)
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
	var out strings.Builder

	for _, rt := range runtimes {
		s := rt.Portfolio.Summary()
		totalEquity = totalEquity.Add(s.Equity)
		totalCash = totalCash.Add(s.Cash)

		// Helm-level labels
		hLabels := map[string]string{
			"helm_id":    rt.HelmID.String(),
			"account_id": rt.AccountID.String(),
		}

		out.WriteString(formatMetric("helm_equity", s.Equity.InexactFloat64(), hLabels))
		out.WriteString(formatMetric("helm_cash", s.Cash.InexactFloat64(), hLabels))
		out.WriteString(formatMetric("helm_drawdown_pct", s.CurrentDD, hLabels))
		out.WriteString(formatMetric("helm_max_drawdown_pct", s.MaxDD, hLabels))
		out.WriteString(formatMetric("helm_daily_pnl", s.DailyPnL.InexactFloat64(), hLabels))
		out.WriteString(formatMetric("helm_win_rate_pct", s.WinRate, hLabels))
		out.WriteString(formatMetric("helm_total_trades", float64(s.TotalTrades), hLabels))
		out.WriteString(formatMetric("helm_open_positions_count", float64(s.OpenPositions), hLabels))

		haltedVal := 0.0
		if rt.RiskMgr.IsHalted() {
			haltedVal = 1.0
		}
		out.WriteString(formatMetric("helm_halted", haltedVal, hLabels))

		// Position-level metrics
		for _, pos := range s.Positions {
			pLabels := map[string]string{
				"helm_id":    rt.HelmID.String(),
				"account_id": rt.AccountID.String(),
				"symbol":     pos.Symbol,
			}
			out.WriteString(formatMetric("helm_position_qty", pos.Qty.InexactFloat64(), pLabels))
			out.WriteString(formatMetric("helm_position_unrealized_pnl", pos.UnrealizedPnL.InexactFloat64(), pLabels))
			out.WriteString(formatMetric("helm_position_market_value", pos.MarketValue.InexactFloat64(), pLabels))
		}

		// Hand-level metrics
		for _, handSummary := range rt.HandSummaries() {
			handLabels := map[string]string{
				"helm_id":    rt.HelmID.String(),
				"account_id": rt.AccountID.String(),
				"hand_id":    handSummary.ID,
				"symbol":     handSummary.Symbol,
			}

			m := handSummary.Metrics
			out.WriteString(formatMetric("helm_hand_signals_received", float64(m.SignalsReceived), handLabels))
			out.WriteString(formatMetric("helm_hand_signals_filtered", float64(m.SignalsFiltered), handLabels))
			out.WriteString(formatMetric("helm_hand_signals_dropped", float64(m.SignalsDropped), handLabels))
			out.WriteString(formatMetric("helm_hand_trades_approved", float64(m.TradesApproved), handLabels))
			out.WriteString(formatMetric("helm_hand_orders_placed", float64(m.OrdersPlaced), handLabels))
			out.WriteString(formatMetric("helm_hand_orders_filled", float64(m.OrdersFilled), handLabels))
			out.WriteString(formatMetric("helm_hand_orders_failed", float64(m.OrdersFailed), handLabels))
			out.WriteString(formatMetric("helm_hand_pnl", m.TotalPnL.InexactFloat64(), handLabels))
			out.WriteString(formatMetric("helm_hand_wins", float64(m.WinCount), handLabels))
			out.WriteString(formatMetric("helm_hand_losses", float64(m.LossCount), handLabels))
			out.WriteString(formatMetric("helm_hand_signal_lag_last_ms", float64(m.LatestSignalLagMs), handLabels))
			out.WriteString(formatMetric("helm_hand_signal_queue_depth", float64(m.SignalQueueDepth), handLabels))

			runningVal := 0.0
			if handSummary.Status != "stopped" && handSummary.Status != "error" {
				runningVal = 1.0
			}
			out.WriteString(formatMetric("helm_hand_running_status", runningVal, handLabels))
		}

		// Exchange latency & error metrics (populated by MeteredExchange wrapper)
		if snap := rt.ExchangeSnapshot(); snap != nil {
			exLabels := map[string]string{
				"helm_id":    rt.HelmID.String(),
				"account_id": rt.AccountID.String(),
				"exchange":   snap.Name,
			}
			out.WriteString(formatMetric("helm_exchange_place_order_calls_total", float64(snap.PlaceOrder.Calls), exLabels))
			out.WriteString(formatMetric("helm_exchange_place_order_errors_total", float64(snap.PlaceOrder.Errors), exLabels))
			out.WriteString(formatMetric("helm_exchange_place_order_latency_avg_ms", snap.PlaceOrder.AvgMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_place_order_latency_max_ms", snap.PlaceOrder.MaxMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_get_order_calls_total", float64(snap.GetOrder.Calls), exLabels))
			out.WriteString(formatMetric("helm_exchange_get_order_errors_total", float64(snap.GetOrder.Errors), exLabels))
			out.WriteString(formatMetric("helm_exchange_get_order_latency_avg_ms", snap.GetOrder.AvgMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_get_order_latency_max_ms", snap.GetOrder.MaxMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_cancel_order_calls_total", float64(snap.CancelOrder.Calls), exLabels))
			out.WriteString(formatMetric("helm_exchange_cancel_order_errors_total", float64(snap.CancelOrder.Errors), exLabels))
			out.WriteString(formatMetric("helm_exchange_cancel_order_latency_avg_ms", snap.CancelOrder.AvgMs, exLabels))
			out.WriteString(formatMetric("helm_exchange_ping_last_ms", snap.PingLastMs, exLabels))
		}
	}

	// Global/summary metrics
	out.WriteString(formatMetric("helm_total_equity", totalEquity.InexactFloat64(), nil))
	out.WriteString(formatMetric("helm_total_cash", totalCash.InexactFloat64(), nil))
	out.WriteString(formatMetric("helm_running_hands", float64(len(h.handMgr.RunningHands())), nil))
	out.WriteString(formatMetric("helm_active_runtimes", float64(len(runtimes)), nil))

	// Signal dispatcher & routing metrics
	ds := h.reg.DispatchStats()
	out.WriteString(formatMetric("helm_dispatch_route_no_helm_total", float64(ds.RouteNoHelm), nil))
	out.WriteString(formatMetric("helm_dispatch_route_no_hand_total", float64(ds.RouteNoHand), nil))

	ns := h.reg.NATSStats()
	out.WriteString(formatMetric("helm_nats_signals_total", float64(ns.SignalsTotal), nil))
	out.WriteString(formatMetric("helm_nats_signals_dispatched_total", float64(ns.SignalsDispatched), nil))
	out.WriteString(formatMetric("helm_nats_signals_missing_id_total", float64(ns.SignalsMissingID), nil))
	out.WriteString(formatMetric("helm_nats_signals_nil_payload_total", float64(ns.SignalsNilPayload), nil))

	c.String(http.StatusOK, out.String())
}

func formatMetric(name string, val float64, labels map[string]string) string {
	if len(labels) == 0 {
		return name + " " + formatFloat(val) + "\n"
	}
	var parts []string
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%q", k, v))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%s{%s} %s\n", name, strings.Join(parts, ","), formatFloat(val))
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
