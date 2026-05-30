package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/eventlog"
	"mallow/helm/internal/module/analytics/domain"
	analyticsservice "mallow/helm/internal/module/analytics/service"
	handdomain "mallow/helm/internal/module/hand/domain"
	dto "mallow/helm/internal/module/hand/dto"
	helmDto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/readmodel"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

type Handler struct {
	handMgr   HandService
	helmSvc   HelmService
	reg       RuntimeRegistry
	eventLog  eventlog.Log // nil = eventlog not available (dev/test without DB)
	analytics *analyticsservice.Service
}

func New(handMgr HandService, helmSvc HelmService, reg RuntimeRegistry, evLog eventlog.Log, analytics *analyticsservice.Service) *Handler {
	return &Handler{handMgr: handMgr, helmSvc: helmSvc, reg: reg, eventLog: evLog, analytics: analytics}
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
		b.GET("/:id/stats", h.stats)
		b.GET("/:id/equity", h.equity)
		b.POST("/:id/start", h.start)
		b.POST("/:id/stop", h.stop)
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

// CapitalOverflow is returned (as the 422 JSON body) when a new or updated hand
// would exceed the helm's unallocated capital. It carries actionable suggestions
// so the caller knows exactly which hands can be reduced and by how much.
//
// Field meanings (all in the helm's quote currency, see docs/metrics-and-reports.md):
//
//	helm_equity      = Portfolio.Equity()                                         — broker truth
//	total_allocated  = Σ existing_hand.AllocatedCapital (excluding the hand being updated)
//	requested        = the AllocatedCapital being created / updated to
//	available        = helm_cash − total_allocated                                — i.e. UnallocatedCapital
type CapitalOverflow struct {
	Error       string              `json:"error"`
	HelmEquity  float64             `json:"helm_equity"`
	TotalAlloc  float64             `json:"total_allocated"`
	Requested   float64             `json:"requested"`
	Available   float64             `json:"available"`
	Suggestions []CapitalSuggestion `json:"suggestions"`
}

// checkCapitalAllocation validates that the requested allocation fits inside
// the helm's unallocated capital budget.
//
// Inputs:
//
//	helmEquity        — `rt.Portfolio.Summary().Cash` (liquid broker cash)
//	existing          — current allocations across all hands on the helm
//	allocatedCapital  — the AllocatedCapital being requested (create or update target)
//	excludeHandID     — pass the current hand's ID when updating so we don't
//	                    double-count its existing allocation; pass "" when creating
//
// Skipped when `allocatedCapital` is zero (shared-pool hand — no per-hand budget).
//
// Returns (*CapitalOverflow, nil) on insufficient budget; (nil, nil) when valid.
func checkCapitalAllocation(
	helmEquity float64,
	existing []handdomain.HandSummary,
	allocatedCapital decimal.Decimal,
	excludeHandID string,
) (*CapitalOverflow, error) {
	if !allocatedCapital.IsPositive() {
		// Shared-pool hand or zero alloc — nothing to validate.
		return nil, nil
	}
	var used float64
	for _, b := range existing {
		if b.ID.String() == excludeHandID {
			continue
		}
		alloc := b.AllocatedCapital.InexactFloat64()
		if alloc > 0 {
			used += alloc
		}
	}
	available := helmEquity - used
	if available < 0 {
		// Pre-existing skew: total alloc already exceeds equity. Clamp the
		// reported "available" to 0 so the error message is intelligible.
		available = 0
	}
	requesting := allocatedCapital.InexactFloat64()
	if requesting <= available {
		return nil, nil
	}
	shortage := requesting - available
	return buildOverflow(
		fmt.Sprintf("insufficient unallocated capital: requesting %.2f but only %.2f available "+
			"(helm equity %.2f, already allocated %.2f)", requesting, available, helmEquity, used),
		helmEquity, used, requesting, available, shortage, existing, excludeHandID,
	), nil
}

// buildOverflow assembles a CapitalOverflow with sorted suggestions.
// Hands with the most reducible free capital are listed first.
func buildOverflow(
	msg string,
	helmEquity, usedAbs, requesting, available, shortage float64,
	existing []handdomain.HandSummary,
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
// @Success 201 {object} shared.SuccessResponse[handdomain.HandSummary]
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
	if overflow, _ := checkCapitalAllocation(rt.Portfolio.Summary().Cash.InexactFloat64(), h.handMgr.ListByHelm(helmID), cfg.AllocatedCapital, ""); overflow != nil {
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
// @Success 200 {object} shared.SuccessResponse[[]handdomain.HandSummary]
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
	// ?live=true → only return hands currently wired into a HelmRuntime.
	// Terminal (killed/released) hands are excluded; the client caches them locally.
	liveOnly := c.Query("live") == "true"

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
		var result []handdomain.HandSummary
		if liveOnly {
			result = h.handMgr.ListByHelmLive(helmID)
		} else {
			result = h.handMgr.ListByHelm(helmID)
		}
		shared.RespondWithSuccess(c, http.StatusOK, "Hands retrieved successfully", result)
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
		var result []handdomain.HandSummary
		if liveOnly {
			result = h.handMgr.ListByHelmLive(helm.ID)
		} else {
			result = h.handMgr.ListByHelm(helm.ID)
		}
		shared.RespondWithSuccess(c, http.StatusOK, "Hands retrieved successfully", result)
		return
	}
	// Without helm_id/account_id filter, list all hands the user can access.
	helms, err := h.helmSvc.ListByUser(userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	var hands []handdomain.HandSummary
	for _, o := range helms {
		if liveOnly {
			hands = append(hands, h.handMgr.ListByHelmLive(o.ID)...)
		} else {
			hands = append(hands, h.handMgr.ListByHelm(o.ID)...)
		}
	}
	if hands == nil {
		hands = []handdomain.HandSummary{}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hands retrieved successfully", hands)
}

// get godoc
// @Summary Get hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[handdomain.HandSummary]
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
// @Success 200 {object} shared.SuccessResponse[handdomain.HandSummary]
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
		if overflow, _ := checkCapitalAllocation(rt.Portfolio.Summary().Cash.InexactFloat64(), h.handMgr.ListByHelm(helmID), patch.AllocatedCapital, id.String()); overflow != nil {
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
	f := readmodel.EventFilter{
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
		events = []readmodel.EventRecord{}
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
// stats returns aggregated KPIs over closed trades for a hand.
// @Summary Per-hand KPIs
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Param period query string false "Preset: 24h|7d|30d|mtd|ytd|all"
// @Param after  query string false "RFC3339 inclusive lower bound"
// @Param before query string false "RFC3339 exclusive upper bound"
// @Success 200 {object} shared.SuccessResponse[helmDto.StatsResp]
// @Router /api/v1/hands/{id}/stats [get]
func (h *Handler) stats(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, _, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	result, err := h.analytics.ComputeStats(c.Request.Context(), analyticsservice.StatsParams{
		Scope:  domain.Scope{UserID: userID, HandID: &id},
		Period: parseHandPeriod(c),
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Stats retrieved", helmDto.StatsToResp(result.Stats, result.Metadata))
}

// equity returns the forward-filled equity curve for a hand.
// @Summary Per-hand equity curve
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param id path string true "Hand ID"
// @Param period query string false "Preset: 24h|7d|30d|mtd|ytd|all"
// @Param resolution query string false "Bucket size: 1m|5m|15m|1h|4h|1d (default 1m)"
// @Success 200 {object} shared.SuccessResponse[helmDto.EquityPageResp]
// @Router /api/v1/hands/{id}/equity [get]
func (h *Handler) equity(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, helmID, err := h.checkHandOwner(c.Param("id"), userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	res := domain.Resolution(c.DefaultQuery("resolution", string(domain.Res1m)))
	result, err := h.analytics.EquityCurve(c.Request.Context(), analyticsservice.EquityCurveParams{
		Scope:      domain.Scope{UserID: userID, HelmID: &helmID, HandID: &id},
		Period:     parseHandPeriod(c),
		Resolution: res,
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := helmDto.EquityPageResp{
		Points:   make([]helmDto.EquityPointResp, 0, len(result.Points)),
		Limit:    len(result.Points),
		Metadata: helmDto.MetadataResp(result.Metadata),
	}
	for _, p := range result.Points {
		resp.Points = append(resp.Points, helmDto.EquityPointResp{
			HandID:        id.String(),
			TS:            p.TS,
			Equity:        p.Equity.InexactFloat64(),
			Cash:          p.Cash.InexactFloat64(),
			RealizedPnL:   p.RealizedPnL.InexactFloat64(),
			UnrealizedPnL: p.UnrealizedPnL.InexactFloat64(),
		})
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Equity retrieved", resp)
}

// parseHandPeriod mirrors the helm-side parsePeriod but lives in the hand
// package so handlers stay decoupled from each other.
func parseHandPeriod(c *gin.Context) domain.Period {
	p := domain.Period{Preset: domain.PeriodPreset(c.Query("period"))}
	if afterStr := c.Query("after"); afterStr != "" {
		if t, err := time.Parse(time.RFC3339, afterStr); err == nil {
			p.After = t
		}
	}
	if beforeStr := c.Query("before"); beforeStr != "" {
		if t, err := time.Parse(time.RFC3339, beforeStr); err == nil {
			p.Before = t
		}
	}
	return p
}

// trades returns closed round-trip trades for a hand. Delegates to the analytics
// service (PostgreSQL-backed, full analytical fields). See docs/metrics-and-reports.md §4.
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
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	period := domain.Period{Preset: domain.PeriodPreset(c.Query("period"))}
	if afterStr := c.Query("after"); afterStr != "" {
		if t, perr := time.Parse(time.RFC3339, afterStr); perr == nil {
			period.After = t
		}
	}
	if beforeStr := c.Query("before"); beforeStr != "" {
		if t, perr := time.Parse(time.RFC3339, beforeStr); perr == nil {
			period.Before = t
		}
	}
	result, err := h.analytics.ListTrades(c.Request.Context(), analyticsservice.ListTradesParams{
		Scope:  domain.Scope{UserID: userID, HandID: &id},
		Period: period,
		Limit:  limit,
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := helmDto.TradesPageResp{
		Trades:   make([]helmDto.TradeResp, 0, len(result.Trades)),
		HasMore:  result.HasMore,
		Next:     result.Next,
		Limit:    limit,
		Metadata: helmDto.MetadataResp(result.Metadata),
	}
	for _, r := range result.Trades {
		resp.Trades = append(resp.Trades, helmDto.TradelogToResp(r))
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Trades retrieved", resp)
}

// Metrics serves the Prometheus exposition. The text rendering lives in
// renderPrometheus (metrics_render.go); this handler is just the HTTP adapter.
func (h *Handler) Metrics(c *gin.Context) {
	c.Header("Content-Type", "text/plain; version=0.0.4")
	c.String(http.StatusOK, renderPrometheus(h.reg, len(h.handMgr.RunningHands())))
}

// allocateCapital godoc
// @Summary Allocate capital
// @Tags hands
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Hand ID"
// @Param request body dto.AllocateCapitalReq true "Capital allocation request"
// @Success 200 {object} shared.SuccessResponse[handdomain.HandSummary]
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

	if overflow, _ := checkCapitalAllocation(rt.Portfolio.Summary().Cash.InexactFloat64(), h.handMgr.ListByHelm(helmID), newCapital, id.String()); overflow != nil {
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
