package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/journal/eventlog"
	"mallow/helm/internal/module/analytics/domain"
	analyticsservice "mallow/helm/internal/module/analytics/service"
	handdomain "mallow/helm/internal/module/hand/domain"
	dto "mallow/helm/internal/module/hand/dto"
	handservice "mallow/helm/internal/module/hand/service"
	helmDto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/shared"
)

type Handler struct {
	handService HandService
	helmService HelmService
	registry    RuntimeRegistry
	eventLog    eventlog.Log // nil = eventlog not available (dev/test without DB)
	analytics   *analyticsservice.Service
}

func New(handMgr HandService, helmSvc HelmService, reg RuntimeRegistry, evLog eventlog.Log, analytics *analyticsservice.Service) *Handler {
	return &Handler{handService: handMgr, helmService: helmSvc, registry: reg, eventLog: evLog, analytics: analytics}
}

// ownedHelm parses :helmId from the path and verifies the caller owns it.
// Used by collection routes (create, list).
func (h *Handler) ownedHelm(c *gin.Context, userID uuid.UUID) (uuid.UUID, bool) {
	helmID, err := uuid.Parse(c.Param("helmId"))
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return uuid.Nil, false
	}
	if err := h.helmService.CheckOwner(helmID, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return uuid.Nil, false
	}
	return helmID, true
}

// ownedHand parses :helmId + :id from the path and verifies the caller owns the helm.
// Ownership is established by the helm alone — the hand is operated on through the
// helm's runtime, so a hand belonging to another helm simply won't be found there.
func (h *Handler) ownedHand(c *gin.Context, userID uuid.UUID) (handID, helmID uuid.UUID, ok bool) {
	helmID, ok = h.ownedHelm(c, userID)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	handID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return uuid.Nil, uuid.Nil, false
	}
	return handID, helmID, true
}

func (h *Handler) Register(rg *gin.RouterGroup) {
	// Batch list across several helms in one request (no client-side N+1 fan-out):
	//   GET /hands?helm_ids=a,b,c[&live=true]
	// Ownership is checked per helm id; unowned ids are silently skipped.
	rg.GET("/hands", h.listBatch)

	// Hands are otherwise addressed through their owning helm: /hands/:helmId/...
	// The client must supply both helmId and the hand id; ownership is checked on
	// helmId and the hand is resolved via that helm's runtime.
	b := rg.Group("/hands/:helmId")
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

// listBatch godoc
// @Summary List hands across several helms in one request
// @Description Returns hands for every helm id in ?helm_ids (CSV) the caller owns,
// @Description merged into one list. Pass ?live=true for runtime-only hands.
// @Description Unowned/invalid helm ids are skipped. Avoids client-side N+1.
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helm_ids query string true "Comma-separated helm IDs"
// @Param live query bool false "Only hands live in the runtime (excludes terminal)"
// @Success 200 {object} shared.SuccessResponse[[]handdomain.HandSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/hands [get]
func (h *Handler) listBatch(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	raw := c.Query("helm_ids")
	if raw == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "helm_ids is required")
		return
	}
	owned := make([]uuid.UUID, 0)
	for _, part := range strings.Split(raw, ",") {
		helmID, err := uuid.Parse(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		if err := h.helmService.CheckOwner(helmID, userID); err != nil {
			continue // skip helms the caller doesn't own
		}
		owned = append(owned, helmID)
	}
	result := h.handService.ListByHelms(owned, c.Query("live") == "true")
	if result == nil {
		result = []handdomain.HandSummary{}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hands retrieved successfully", result)
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
	userID, ok := shared.CallerUserID(c)
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
	helmID, ok := h.ownedHelm(c, userID)
	if !ok {
		return
	}
	req.HelmID = helmID
	cfg := req.ToDomain()
	rt, err := h.registry.Get(helmID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "helm runtime not available")
		return
	}
	if overflow, _ := handservice.CheckCapitalAllocation(rt.Portfolio.Summary().Cash.InexactFloat64(), h.handService.ListByHelm(helmID), cfg.AllocatedCapital, ""); overflow != nil {
		c.JSON(http.StatusUnprocessableEntity, overflow)
		return
	}
	// multi-hand same-symbol allowed — net qty vs per-hand qty gap deferred
	// if err := handservice.CheckSymbolConflict(h.handMgr.ListByHelm(helmID), cfg.Symbols, ""); err != nil {
	// 	shared.RespondWithError(c, http.StatusConflict, err.Error())
	// 	return
	// }
	instance, err := h.handService.Create(cfg)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusCreated, "Hand created successfully", instance)
}

// list godoc
// @Summary List hands for a helm
// @Description Returns every hand of the helm, including terminal (killed/released)
// @Description hands restored from the DB. Pass ?live=true to return only hands
// @Description currently wired into the runtime (terminal hands excluded).
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param live query bool false "Only return hands live in the runtime (excludes terminal)"
// @Success 200 {object} shared.SuccessResponse[[]handdomain.HandSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId} [get]
func (h *Handler) list(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	helmID, ok := h.ownedHelm(c, userID)
	if !ok {
		return
	}
	// ?live=true → only return hands currently wired into a HelmRuntime.
	// Terminal (killed/released) hands are excluded; the client caches them locally.
	var result []handdomain.HandSummary
	if c.Query("live") == "true" {
		result = h.handService.ListByHelmLive(helmID)
	} else {
		result = h.handService.ListByHelm(helmID)
	}
	if result == nil {
		result = []handdomain.HandSummary{}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hands retrieved successfully", result)
}

// get godoc
// @Summary Get hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[handdomain.HandSummary]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId}/{id} [get]
func (h *Handler) get(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	handID, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}
	summary, err := h.handService.Get(handID)
	if err != nil || summary.HelmID != helmID {
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
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Param request body dto.UpdateHandReq true "Hand update request"
// @Success 200 {object} shared.SuccessResponse[handdomain.HandSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId}/{id} [put]
func (h *Handler) update(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}

	var req dto.UpdateHandReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Confirm the hand belongs to the helm in the path before mutating.
	if cur, err := h.handService.Get(id); err != nil || cur.HelmID != helmID {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}

	patch := req.ToDomain()

	if err := h.handService.Update(id, patch); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	summary, _ := h.handService.Get(id)
	shared.RespondWithSuccess(c, http.StatusOK, "Hand updated successfully", summary)
}

// start godoc
// @Summary Start hand
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId}/{id}/start [post]
func (h *Handler) start(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}
	if err := h.handService.Start(id, helmID); err != nil {
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
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId}/{id}/stop [post]
func (h *Handler) stop(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}
	if err := h.handService.Stop(id, helmID); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand stopped successfully", dto.HandActionResp{Status: "stopped", ID: id.String()})
}

// kill godoc
// @Summary Kill hand (stop + flatten all positions at market)
// @Description Emergency stop: deregisters the hand, cancels open orders, and places market close orders for all active positions. The hand becomes terminal (killed) and cannot be restarted.
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId}/{id}/kill [post]
func (h *Handler) kill(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}
	if err := h.handService.Kill(c.Request.Context(), id, helmID); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand killed successfully", dto.HandActionResp{Status: "killed", ID: id.String()})
}

// release godoc
// @Summary Release a hand (orphan open positions)
// @Description Stop the hand without closing positions. Open legs are marked orphaned in the poslog; the hand will not reclaim them on restart.
// @Tags hands
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Success 200 {object} shared.SuccessResponse[dto.HandActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId}/{id}/release [post]
func (h *Handler) release(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}
	if err := h.handService.Release(c.Request.Context(), id, helmID); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Hand released successfully", dto.HandActionResp{Status: "released", ID: id.String()})
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
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Param limit query int false "Max events to return" default(100)
// @Param before query string false "Return events before this RFC3339 timestamp (pagination cursor)"
// @Success 200 {object} shared.SuccessResponse[[]eventlog.EventRecord]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId}/{id}/activity [get]
func (h *Handler) activity(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	handID, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}
	if h.eventLog == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "activity log not available")
		return
	}

	limit := parseLimitQuery(c, 100, 500)
	f := eventlog.EventFilter{
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
		events = []eventlog.EventRecord{}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Activity retrieved", events)
}

// trades godoc
// @Summary List closed trades for a hand (poslog-backed, cursor paging)
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Param cursor query int false "Cursor from previous page (0 = start)" default(0)
// @Param limit query int false "Page size" default(100)
// @Success 200 {object} shared.SuccessResponse[helmDto.TradesPageResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/hands/{helmId}/{id}/trades [get]
// stats returns aggregated KPIs over closed trades for a hand.
// @Summary Per-hand KPIs
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Param period query string false "Preset: 24h|7d|30d|mtd|ytd|all"
// @Param after  query string false "RFC3339 inclusive lower bound"
// @Param before query string false "RFC3339 exclusive upper bound"
// @Success 200 {object} shared.SuccessResponse[helmDto.StatsResp]
// @Router /api/v1/hands/{helmId}/{id}/stats [get]
func (h *Handler) stats(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, _, ok := h.ownedHand(c, userID)
	if !ok {
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

// HandEquityPoint is one sample on a hand's cumulative PnL curve, projected from
// its closed-trade history (newest trade last).
type HandEquityPoint struct {
	TS        time.Time       `json:"ts"`
	CumPnL    decimal.Decimal `json:"cum_pnl"`
	CumNetPnL decimal.Decimal `json:"cum_net_pnl"`
}

// equity returns a running cumulative PnL curve for a hand derived from its trade history.
// @Summary Per-hand PnL curve (projection from trades)
// @Tags hands
// @Security BearerAuth
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Param period query string false "Preset: 24h|7d|30d|mtd|ytd|all"
// @Success 200 {object} shared.SuccessResponse[[]HandEquityPoint]
// @Router /api/v1/hands/{helmId}/{id}/equity [get]
func (h *Handler) equity(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}
	result, err := h.analytics.ListTrades(c.Request.Context(), analyticsservice.ListTradesParams{
		Scope:  domain.Scope{UserID: userID, HelmID: &helmID, HandID: &id},
		Period: parseHandPeriod(c),
		Limit:  5000,
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	points := make([]HandEquityPoint, 0, len(result.Trades))
	cumPnL := decimal.Zero
	cumNet := decimal.Zero
	for i := len(result.Trades) - 1; i >= 0; i-- {
		t := result.Trades[i]
		cumPnL = cumPnL.Add(t.PnL)
		cumNet = cumNet.Add(t.NetPnL)
		points = append(points, HandEquityPoint{TS: t.ExitAt, CumPnL: cumPnL, CumNetPnL: cumNet})
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Equity retrieved", points)
}

// parseLimitQuery parses ?limit= with a default and a hard maximum.
// Returns defaultVal on parse error or out-of-range input.
func parseLimitQuery(c *gin.Context, defaultVal, maxVal int) int {
	n, err := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(defaultVal)))
	if err != nil || n <= 0 {
		return defaultVal
	}
	if n > maxVal {
		return maxVal
	}
	return n
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
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, _, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}
	limit := parseLimitQuery(c, 100, 500)
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
	c.String(http.StatusOK, renderPrometheus(h.registry, h.handService.RunningHandCount()))
}

// allocateCapital godoc
// @Summary Allocate capital
// @Tags hands
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param helmId path string true "Helm ID"
// @Param id path string true "Hand ID"
// @Param request body dto.AllocateCapitalReq true "Capital allocation request"
// @Success 200 {object} shared.SuccessResponse[handdomain.HandSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 422 {object} dto.CapitalOverflow
// @Router /api/v1/hands/{helmId}/{id}/allocate-capital [post]
func (h *Handler) allocateCapital(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, helmID, ok := h.ownedHand(c, userID)
	if !ok {
		return
	}

	var req dto.AllocateCapitalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	summary, err := h.handService.Get(id)
	if err != nil || summary.HelmID != helmID {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}

	delta := decimal.NewFromFloat(req.Amount)
	newCapital := summary.AllocatedCapital.Add(delta)

	if !newCapital.IsPositive() {
		shared.RespondWithError(c, http.StatusBadRequest, "new allocated capital must be greater than zero")
		return
	}

	// Validate capital allocation against helm budget
	rt, err := h.registry.Get(helmID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "helm runtime not available")
		return
	}

	if overflow, _ := handservice.CheckCapitalAllocation(rt.Portfolio.Summary().Cash.InexactFloat64(), h.handService.ListByHelm(helmID), newCapital, id.String()); overflow != nil {
		c.JSON(http.StatusUnprocessableEntity, overflow)
		return
	}

	if _, err = h.handService.AllocateCapital(id, helmID, delta); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Fetch updated details
	summary, _ = h.handService.Get(id)
	shared.RespondWithSuccess(c, http.StatusOK, "Capital allocated successfully", summary)
}
