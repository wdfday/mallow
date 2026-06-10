package handler

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/eventlog"
	"mallow/helm/internal/infra/orderlog"
	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/poslog"
	"mallow/helm/internal/module/analytics/domain"
	analyticsservice "mallow/helm/internal/module/analytics/service"
	dto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/readmodel"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/runtime/perf"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

// parsePeriod extracts an analytics Period from query params.
// Supports:
//
//	period=24h|7d|30d|mtd|ytd|all   (preset)
//	after=RFC3339, before=RFC3339   (explicit bounds; either or both)
//
// Empty inputs → zero Period (means "all").
func parsePeriod(c *gin.Context) domain.Period {
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

type Handler struct {
	svc       HelmService
	handMgr   HandManager
	reg       *runtime.Registry
	nc        *nats.Conn
	fillLog   *perflog.FillLog
	posLog    poslog.Log
	orderLog  orderlog.Log
	analytics *analyticsservice.Service
	eventLog  eventlog.Log
}

func New(
	svc HelmService,
	handMgr HandManager,
	reg *runtime.Registry,
	nc *nats.Conn,
	fillLog *perflog.FillLog,
	posLog poslog.Log,
	orderLog orderlog.Log,
	analytics *analyticsservice.Service,
	eventLog eventlog.Log,
) *Handler {
	return &Handler{
		svc:       svc,
		handMgr:   handMgr,
		reg:       reg,
		nc:        nc,
		fillLog:   fillLog,
		posLog:    posLog,
		orderLog:  orderLog,
		analytics: analytics,
		eventLog:  eventLog,
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
		o.GET("/:id/stats", h.stats)
		o.GET("/:id/orders", h.orders)
		o.GET("/:id/orders/history", h.ordersHistory)
		o.GET("/:id/events/history", h.eventsHistory)
		o.GET("/:id/events", h.events)

		ex := o.Group("/:id/exchange")
		{
			ex.GET("/account", h.exchangeAccount)
			ex.GET("/price", h.exchangePrice)
			ex.GET("/metrics", h.exchangeMetrics)
			ex.GET("/ping", h.exchangePing)
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
	halted := false
	var lastSyncAt *time.Time
	if rtErr == nil {
		paused = rt.IsPaused()
		halted = rt.IsHalted()
		if t := rt.LastSyncAt(); !t.IsZero() {
			lastSyncAt = &t
		}
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Helm retrieved successfully", dto.HelmDetailResp{
		HelmResp:   dto.HelmToResp(cfg),
		Hands:      hands,
		Running:    rtErr == nil,
		Paused:     paused,
		Halted:     halted,
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
// @Summary Disable orchestrator — flatten all hand positions and disable
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
	if err := h.svc.Disable(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator disabled successfully", dto.ActionResp{Status: "disabled", ID: id})
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
	hands := h.handMgr.ListByHelm(rt.HelmID)
	shared.RespondWithSuccess(c, http.StatusOK, "Portfolio retrieved successfully",
		dto.PortfolioToResp(rt.PortfolioSummary(), dto.SumAllocatedCapital(hands), dto.SumAvailableCash(hands)))
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
// @Summary List closed trades for a helm (PostgreSQL-backed, all analytical fields)
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Param before query string false "RFC3339 exit_at cursor (exclusive); omit for newest"
// @Param limit query int false "Page size (max 500)" default(100)
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
	period := parsePeriod(c)
	result, err := h.analytics.ListTrades(c.Request.Context(), analyticsservice.ListTradesParams{
		Scope:  domain.Scope{UserID: userID, HelmID: &id},
		Period: period,
		Limit:  limit,
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := dto.TradesPageResp{
		Trades:   make([]dto.TradeResp, 0, len(result.Trades)),
		HasMore:  result.HasMore,
		Next:     result.Next,
		Limit:    limit,
		Metadata: dto.MetadataResp(result.Metadata),
	}
	for _, r := range result.Trades {
		resp.Trades = append(resp.Trades, dto.TradelogToResp(r))
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Trades retrieved successfully", resp)
}

// eventsHistory godoc
// @Summary Paged helm event history (newest-first, backward time cursor)
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Param hand_id query string false "Filter to a single hand"
// @Param before query string false "RFC3339 cursor (exclusive upper bound); omit for latest"
// @Param limit query int false "Page size" default(100)
// @Success 200 {object} shared.SuccessResponse[dto.EventsPageResp]
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/events/history [get]
func (h *Handler) eventsHistory(c *gin.Context) {
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
	if h.eventLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "event log unavailable")
		return
	}

	_, limit := parsePage(c)
	filter := readmodel.EventFilter{HelmID: id, Limit: limit}
	if hs := c.Query("hand_id"); hs != "" {
		if hid, perr := uuid.Parse(hs); perr == nil {
			filter.HandID = &hid
		}
	}
	if bs := c.Query("before"); bs != "" {
		if t, perr := time.Parse(time.RFC3339, bs); perr == nil {
			filter.Before = t
		}
	}

	events, err := h.eventLog.Query(c.Request.Context(), filter)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}

	// Events come newest-first. A full page implies there may be older rows; the
	// next page starts before the oldest row returned (backward time cursor).
	resp := dto.EventsPageResp{Events: events, Limit: limit}
	if limit > 0 && len(events) == limit {
		resp.HasMore = true
		resp.Next = events[len(events)-1].At.Format(time.RFC3339Nano)
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Events retrieved successfully", resp)
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
// stats godoc
// @Summary Aggregated KPIs over closed trades for a helm
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Param period query string false "Preset: 24h|7d|30d|mtd|ytd|all"
// @Param after query string false "RFC3339 inclusive lower bound (overrides preset.after)"
// @Param before query string false "RFC3339 exclusive upper bound (overrides preset.before)"
// @Success 200 {object} shared.SuccessResponse[dto.StatsResp]
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/stats [get]
func (h *Handler) stats(c *gin.Context) {
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
	result, err := h.analytics.ComputeStats(c.Request.Context(), analyticsservice.StatsParams{
		Scope:  domain.Scope{UserID: userID, HelmID: &id},
		Period: parsePeriod(c),
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Stats retrieved successfully", dto.StatsToResp(result.Stats, result.Metadata))
}

// snapshots returns raw equity_snapshots rows for the helm (PG-backed, no bucketing).
// FE that needs an evenly-spaced curve should call /equity instead.
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
	_, limit := parsePage(c)
	params := analyticsservice.ListSnapshotsParams{
		Scope: domain.Scope{UserID: userID, HelmID: &id},
		Limit: limit,
	}
	if beforeStr := c.Query("before"); beforeStr != "" {
		if t, perr := time.Parse(time.RFC3339, beforeStr); perr == nil {
			params.Before = t
		}
	}
	result, err := h.analytics.ListSnapshots(c.Request.Context(), params)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := dto.SnapshotPageResp{
		Snapshots: make([]dto.SnapshotResp, 0, len(result.Snapshots)),
		HasMore:   result.HasMore,
		Next:      result.Next,
		Limit:     limit,
		Metadata:  dto.MetadataResp(result.Metadata),
	}
	for _, s := range result.Snapshots {
		resp.Snapshots = append(resp.Snapshots, dto.SnapshotRowToResp(s))
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
// equity returns a forward-filled equity curve for the helm. Backed by the
// analytics service (PG `equity_snapshots`); resolution defaults to 1m.
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
	res := domain.Resolution(c.DefaultQuery("resolution", string(domain.Res1m)))
	result, err := h.analytics.EquityCurve(c.Request.Context(), analyticsservice.EquityCurveParams{
		Scope:      domain.Scope{UserID: userID, HelmID: &id},
		Period:     parsePeriod(c),
		Resolution: res,
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	resp := dto.EquityPageResp{
		Points:   make([]dto.EquityPointResp, 0, len(result.Points)),
		HasMore:  false, // bucket grid is bounded by period
		Limit:    len(result.Points),
		Metadata: dto.MetadataResp(result.Metadata),
	}
	for _, p := range result.Points {
		resp.Points = append(resp.Points, dto.EquityPointResp{
			TS:            p.TS,
			Equity:        p.Equity.InexactFloat64(),
			Cash:          p.Cash.InexactFloat64(),
			RealizedPnL:   p.RealizedPnL.InexactFloat64(),
			UnrealizedPnL: p.UnrealizedPnL.InexactFloat64(),
		})
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

// orderHistoryResp is the JSON shape for a persisted order lifecycle record.
type orderHistoryResp struct {
	ExchangeOrderID string `json:"exchange_order_id"`
	ClientOrderID   string `json:"client_order_id,omitempty"`
	HandID          string `json:"hand_id,omitempty"`
	PositionID      string `json:"position_id,omitempty"`
	Symbol          string `json:"symbol"`
	Side            string `json:"side,omitempty"`
	OrderType       string `json:"order_type,omitempty"`
	Qty             string `json:"qty,omitempty"`
	Price           string `json:"price,omitempty"`
	Status          string `json:"status"`
	FilledQty       string `json:"filled_qty,omitempty"`
	FilledPrice     string `json:"filled_price,omitempty"`
	IsClose         bool   `json:"is_close"`
	Reason          string `json:"reason,omitempty"`
	PlacedAt        string `json:"placed_at,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

// ordersHistory returns the durable order lifecycle history from the orders table,
// projected from the poslog by the orders persister. Distinct from /orders which
// returns only the in-memory live order list.
//
// @Summary List persisted order history for a helm
// @Tags helms
// @Produce json
// @Param id path string true "Helm ID"
// @Param hand_id query string false "filter by hand"
// @Param status query string false "filter by status (placed|filled|cancelled)"
// @Param limit query int false "max rows (default 100)"
// @Success 200 {object} shared.SuccessResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/orders/history [get]
func (h *Handler) ordersHistory(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	if h.orderLog == nil {
		shared.RespondWithSuccess(c, http.StatusOK, "Order history unavailable", []orderHistoryResp{})
		return
	}

	f := readmodel.OrderFilter{
		HelmID: rt.HelmID,
		HandID: c.Query("hand_id"),
		Status: c.Query("status"),
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}

	records, err := h.orderLog.Query(c.Request.Context(), f)
	if err != nil {
		slog.Error("order history query failed", "helm_id", rt.HelmID, "err", err)
		shared.RespondWithError(c, http.StatusInternalServerError, "failed to query order history")
		return
	}

	out := make([]orderHistoryResp, 0, len(records))
	for _, r := range records {
		resp := orderHistoryResp{
			ExchangeOrderID: r.ExchangeOrderID,
			ClientOrderID:   r.ClientOrderID,
			HandID:          r.HandID,
			PositionID:      r.PositionID,
			Symbol:          r.Symbol,
			Side:            r.Side,
			OrderType:       r.OrderType,
			Status:          r.Status,
			IsClose:         r.IsClose,
			Reason:          r.Reason,
			UpdatedAt:       r.UpdatedAt.Format(time.RFC3339),
		}
		if r.Qty.IsPositive() {
			resp.Qty = r.Qty.String()
		}
		if r.Price.IsPositive() {
			resp.Price = r.Price.String()
		}
		if r.FilledQty.IsPositive() {
			resp.FilledQty = r.FilledQty.String()
		}
		if r.FilledPrice.IsPositive() {
			resp.FilledPrice = r.FilledPrice.String()
		}
		if !r.PlacedAt.IsZero() {
			resp.PlacedAt = r.PlacedAt.Format(time.RFC3339)
		}
		out = append(out, resp)
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Order history retrieved successfully", out)
}
