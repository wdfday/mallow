package handler

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/poslog"
	dto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/perf"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

type Handler struct {
	svc         HelmService
	handMgr     HandManager
	reg         *runtime.Registry
	nc          *nats.Conn
	fillLog     *perflog.FillLog
	snapshotLog perf.SnapshotLog
	posLog      poslog.Log
}

func New(
	svc HelmService,
	handMgr HandManager,
	reg *runtime.Registry,
	nc *nats.Conn,
	fillLog *perflog.FillLog,
	snapshotLog perf.SnapshotLog,
	posLog poslog.Log,
) *Handler {
	return &Handler{
		svc:         svc,
		handMgr:     handMgr,
		reg:         reg,
		nc:          nc,
		fillLog:     fillLog,
		snapshotLog: snapshotLog,
		posLog:      posLog,
	}
}

func parsePage(c *gin.Context) (page, limit int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ = strconv.Atoi(c.DefaultQuery("limit", "100"))
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 500 {
		limit = 100
	}
	return
}

func callerUserID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(pkgmw.UserID(c))
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, "invalid user")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) Register(rg *gin.RouterGroup) {
	o := rg.Group("/helms")
	{
		o.GET("", h.list)
		o.GET("/:id", h.get)
		o.PUT("/:id", h.update)
		o.POST("/:id/enable", h.enable)
		o.POST("/:id/disable", h.disable)
		o.POST("/:id/pause", h.pause)
		o.POST("/:id/resume", h.resume)
		o.POST("/:id/kill", h.kill)
		o.POST("/:id/halt/reset", h.resetHalt)
		o.GET("/:id/portfolio", h.portfolio)
		o.GET("/:id/positions", h.positions)
		o.GET("/:id/trades", h.trades)
		o.GET("/:id/fills", h.fills)
		o.GET("/:id/snapshots", h.snapshots)
		o.GET("/:id/equity", h.equity)
		o.GET("/:id/orders", h.orders)
		o.GET("/:id/events", h.events)

		ex := o.Group("/:id/exchange")
		{
			ex.GET("/account", h.exchangeAccount)
			ex.GET("/price", h.exchangePrice)
			ex.POST("/orders", h.exchangePlaceOrder)
			ex.GET("/orders", h.exchangeGetOrder)
			ex.DELETE("/orders", h.exchangeCancelOrder)
		}
	}
}

// enable godoc
// @Summary Enable helm
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/enable [post]
func (h *Handler) enable(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.Enable(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator enabled successfully", dto.ActionResp{Status: "enabled", ID: id})
}

// disable godoc
// @Summary Disable helm
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/disable [post]
func (h *Handler) disable(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.Disable(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator disabled successfully", dto.ActionResp{Status: "disabled", ID: id})
}

// list godoc
// @Summary List helms
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Success 200 {object} shared.SuccessResponse[[]dto.HelmResp]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/helms [get]
func (h *Handler) list(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	cfgs, err := h.svc.ListByUser(userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrators retrieved successfully", dto.HelmsToResp(cfgs))
}

// get godoc
// @Summary Get helm
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.HelmDetailResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id} [get]
func (h *Handler) get(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	cfg, err := h.svc.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, err.Error())
		return
	}

	hands := h.handMgr.ListByHelm(id)
	rt, rtErr := h.reg.Get(id)
	paused := false
	var lastSyncAt *time.Time
	if rtErr == nil {
		paused = rt.IsPaused()
		if t := rt.LastSyncAt(); !t.IsZero() {
			lastSyncAt = &t
		}
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Helm retrieved successfully", dto.HelmDetailResp{
		HelmResp:   dto.HelmToResp(cfg),
		Hands:      hands,
		Running:    rtErr == nil,
		Paused:     paused,
		LastSyncAt: lastSyncAt,
	})
}

// update godoc
// @Summary Update orchestrator
// @Tags helms
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Param request body dto.UpdateHelmReq true "Orchestrator update"
// @Success 200 {object} shared.SuccessResponse[dto.HelmResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id} [put]
func (h *Handler) update(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	var req dto.UpdateHelmReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	updateReq := dto.UpdateReq{
		Name: req.Name,
	}
	if req.Portfolio != nil {
		p := req.Portfolio.ToDomain()
		updateReq.Portfolio = &p
	}
	if req.Risk != nil {
		r := req.Risk.ToDomain()
		updateReq.Risk = &r
	}

	updated, err := h.svc.Update(id, updateReq)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator updated successfully", dto.HelmToResp(updated))
}

// pause godoc
// @Summary Pause orchestrator
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/pause [post]
func (h *Handler) pause(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.Pause(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator paused successfully", dto.ActionResp{Status: "paused", ID: id})
}

// resume godoc
// @Summary Resume orchestrator
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/resume [post]
func (h *Handler) resume(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.Resume(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator resumed successfully", dto.ActionResp{Status: "resumed", ID: id})
}

// kill godoc
// @Summary Kill orchestrator — flatten all hand positions and halt
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/kill [post]
func (h *Handler) kill(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.Kill(c.Request.Context(), id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator killed successfully", dto.ActionResp{Status: "halted", ID: id})
}

// resetHalt godoc
// @Summary Reset orchestrator halt flag and restore to active
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/halt/reset [post]
func (h *Handler) resetHalt(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.ResetHalt(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator halt reset successfully", dto.ActionResp{Status: "active", ID: id})
}

// --- Per-orchestrator runtime data ---

// requireRuntime looks up the runtime by path :id without an ownership check.
// Use requireOwnedRuntime for user-facing endpoints.
func (h *Handler) requireRuntime(c *gin.Context) *runtime.HelmRuntime {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return nil
	}
	rt, err := h.reg.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "orchestrator runtime not active")
		return nil
	}
	return rt
}

// requireOwnedRuntime combines ownership check + runtime lookup.
// Returns nil and writes the error response if either check fails.
func (h *Handler) requireOwnedRuntime(c *gin.Context, userID uuid.UUID) *runtime.HelmRuntime {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return nil
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return nil
	}
	rt, err := h.reg.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "orchestrator runtime not active")
		return nil
	}
	return rt
}

// portfolio godoc
// @Summary Get helm portfolio summary
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.PortfolioResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/portfolio [get]
func (h *Handler) portfolio(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Portfolio retrieved successfully", dto.PortfolioToResp(rt.Portfolio.Summary()))
}

// positions godoc
// @Summary List orchestrator positions
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[[]dto.PositionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/positions [get]
func (h *Handler) positions(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Positions retrieved successfully", dto.PositionsToResp(rt.Portfolio.Positions()))
}

// trades godoc
// @Summary List closed trades for a helm (all hands, JetStream-backed)
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Param cursor query int false "JetStream sequence cursor (0 = from start)"
// @Param limit query int false "Page size" default(100)
// @Success 200 {object} shared.SuccessResponse[dto.TradesPageResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/trades [get]
func (h *Handler) trades(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	_, limit := parsePage(c)
	cursorStr := c.DefaultQuery("cursor", "0")
	cursor, _ := strconv.ParseUint(cursorStr, 10, 64)

	if h.posLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "trade log unavailable")
		return
	}

	helm, err := h.svc.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, err.Error())
		return
	}
	// Fan-out: query each hand's trades then merge by ExitAt.
	hands := h.handMgr.ListByHelm(id)
	var all []poslog.TradeRecord
	for _, hs := range hands {
		page, qErr := h.posLog.TradesPaged(c.Request.Context(), helm.ID.String(), hs.ID.String(), cursor, limit)
		if qErr == nil {
			all = append(all, page.Trades...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ExitAt.Before(all[j].ExitAt) })
	hasMore := len(all) > limit
	if hasMore {
		all = all[:limit]
	}
	var next uint64
	if hasMore && len(all) > 0 {
		next = all[len(all)-1].Cursor + 1
	}
	resp := dto.TradesPageResp{
		Trades:  make([]dto.TradeResp, 0, len(all)),
		Next:    next,
		HasMore: hasMore,
		Limit:   limit,
	}
	for _, t := range all {
		resp.Trades = append(resp.Trades, dto.TradeRecordToResp(t))
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Trades retrieved successfully", resp)
}

// fills godoc
// @Summary List fills for a helm account (from TRADE_FILLS JetStream stream)
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Param after query string false "RFC3339 cursor (exclusive); omit for all"
// @Param limit query int false "Page size" default(200)
// @Success 200 {object} shared.SuccessResponse[dto.FillPageResp]
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/fills [get]
func (h *Handler) fills(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if h.fillLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "fill log unavailable")
		return
	}
	helm, err := h.svc.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, err.Error())
		return
	}
	_, limit := parsePage(c)
	page := perf.Page{Limit: limit}
	if afterStr := c.Query("after"); afterStr != "" {
		if t, tErr := time.Parse(time.RFC3339, afterStr); tErr == nil {
			page.After = t
		}
	}
	result, err := h.fillLog.Query(c.Request.Context(), helm.AccountID.String(), page)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := dto.FillPageResp{
		Fills:   make([]dto.FillResp, 0, len(result.Fills)),
		HasMore: result.HasMore,
		Limit:   limit,
	}
	if result.HasMore {
		resp.Next = result.Next.UTC().Format(time.RFC3339)
	}
	for _, f := range result.Fills {
		resp.Fills = append(resp.Fills, dto.FillToResp(f))
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Fills retrieved successfully", resp)
}

// snapshots godoc
// @Summary List portfolio snapshots for a helm (from PORTFOLIO_SNAPSHOTS stream)
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Param after query string false "RFC3339 cursor (exclusive); omit for all"
// @Param limit query int false "Page size" default(100)
// @Success 200 {object} shared.SuccessResponse[dto.SnapshotPageResp]
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/snapshots [get]
func (h *Handler) snapshots(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if h.snapshotLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "portfolio log unavailable")
		return
	}
	_, limit := parsePage(c)
	page := perf.Page{Limit: limit}
	if afterStr := c.Query("after"); afterStr != "" {
		if t, tErr := time.Parse(time.RFC3339, afterStr); tErr == nil {
			page.After = t
		}
	}
	result, err := h.snapshotLog.Query(c.Request.Context(), id.String(), "", page)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := dto.SnapshotPageResp{
		Snapshots: make([]dto.SnapshotResp, 0, len(result.Snapshots)),
		HasMore:   result.HasMore,
		Limit:     limit,
	}
	if result.HasMore {
		resp.Next = result.Next.UTC().Format(time.RFC3339)
	}
	for _, s := range result.Snapshots {
		resp.Snapshots = append(resp.Snapshots, dto.SnapshotToResp(s))
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Snapshots retrieved successfully", resp)
}

// equity godoc
// @Summary Equity curve for a helm (fan-out across all hands, from HELM_EQUITY stream)
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Param after query string false "RFC3339 cursor (exclusive); omit for all"
// @Param limit query int false "Page size" default(200)
// @Success 200 {object} shared.SuccessResponse[dto.EquityPageResp]
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/equity [get]
func (h *Handler) equity(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CheckOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if h.snapshotLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "equity log unavailable")
		return
	}
	_, limit := parsePage(c)
	page := perf.Page{Limit: limit}
	if afterStr := c.Query("after"); afterStr != "" {
		if t, tErr := time.Parse(time.RFC3339, afterStr); tErr == nil {
			page.After = t
		}
	}
	// Fan-out across all hands, merge by TS.
	hands := h.handMgr.ListByHelm(id)
	var all []perf.Snapshot
	for _, hs := range hands {
		result, qErr := h.snapshotLog.Query(c.Request.Context(), id.String(), hs.ID.String(), page)
		if qErr == nil {
			all = append(all, result.Snapshots...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].TS.Before(all[j].TS) })
	hasMore := len(all) > limit
	if hasMore {
		all = all[:limit]
	}
	resp := dto.EquityPageResp{
		Points:  make([]dto.EquityPointResp, 0, len(all)),
		HasMore: hasMore,
		Limit:   limit,
	}
	if hasMore && len(all) > 0 {
		resp.Next = all[len(all)-1].TS.UTC().Format(time.RFC3339)
	}
	for _, p := range all {
		resp.Points = append(resp.Points, dto.EquityPointResp{HandID: p.HandID, TS: p.TS, Equity: p.Equity.InexactFloat64()})
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Equity retrieved successfully", resp)
}

// orders godoc
// @Summary List orchestrator orders
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[[]dto.OrderResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/orders [get]
func (h *Handler) orders(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	id := rt.HelmID
	var allOrders []dto.OrderResp
	for _, bs := range h.handMgr.ListByHelm(id) {
		bi, getErr := h.handMgr.Get(bs.ID)
		if getErr == nil {
			for _, o := range bi.Runner.Orders() {
				allOrders = append(allOrders, dto.OrderToResp(o))
			}
		}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orders retrieved successfully", allOrders)
}
