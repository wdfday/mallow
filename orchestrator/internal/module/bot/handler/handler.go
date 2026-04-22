package handler

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	pkgmw "mallow/pkg/middleware"
	"orchestrator/internal/infra/engine"
	"orchestrator/internal/module/bot/domain"
	"orchestrator/internal/module/bot/service"
	orchdomain "orchestrator/internal/module/orchesrator/domain"
	orchsvc "orchestrator/internal/module/orchesrator/service"
	"orchestrator/internal/runtime"
	"orchestrator/internal/shared"
)

type Handler struct {
	botMgr  *service.Service
	orchSvc *orchsvc.Service
	sigCli  *engine.SignalClient
	reg     *runtime.Registry
}

func New(botMgr *service.Service, orchSvc *orchsvc.Service, sigCli *engine.SignalClient, reg *runtime.Registry) *Handler {
	return &Handler{botMgr: botMgr, orchSvc: orchSvc, sigCli: sigCli, reg: reg}
}

func callerUserID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(pkgmw.UserID(c))
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, "invalid user")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) resolveOrchestratorID(userID uuid.UUID, accountID, orchestratorID uuid.UUID) (uuid.UUID, error) {
	if accountID != uuid.Nil {
		orch, err := h.orchSvc.GetByAccount(accountID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("orchestrator not found")
		}
		if orch.UserID != userID {
			return uuid.Nil, fmt.Errorf("orchestrator not found")
		}
		return orch.ID, nil
	}
	if orchestratorID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("account_id or orchestrator_id is required")
	}
	if err := h.orchSvc.CheckOwner(orchestratorID, userID); err != nil {
		return uuid.Nil, fmt.Errorf("orchestrator not found")
	}
	return orchestratorID, nil
}

// checkBotOwner verifies that the bot's orchestrator belongs to userID.
func (h *Handler) checkBotOwner(botID string, userID uuid.UUID) (uuid.UUID, error) {
	bi, err := h.botMgr.Get(botID)
	if err != nil {
		return uuid.Nil, err
	}
	if err := h.orchSvc.CheckOwner(bi.Data.OrchestratorID, userID); err != nil {
		return uuid.Nil, fmt.Errorf("not found")
	}
	return bi.Data.OrchestratorID, nil
}

func (h *Handler) Register(rg *gin.RouterGroup) {
	b := rg.Group("/bots")
	{
		b.POST("", h.create)
		b.GET("", h.list)
		b.GET("/:id", h.get)
		b.PUT("/:id", h.update)
		b.POST("/:id/start", h.start)
		b.POST("/:id/stop", h.stop)
		b.POST("/:id/restart", h.restart)
		b.POST("/:id/pause", h.pause)
		b.POST("/:id/resume", h.resume)
		b.POST("/:id/kill", h.kill)
	}
	rg.POST("/signal-engine/configure", h.configureStrategy)
}

// checkCapitalAllocation validates that adding newPos to an orchestrator doesn't exceed
// its configured capital. excludeBotID is the bot being updated (skip its existing allocation);
// pass "" when creating a new bot.
func checkCapitalAllocation(orch *orchdomain.OrchestratorConfig, existing []domain.BotSummary, newPos domain.PositionConfig, excludeBotID string) error {
	// ── Fixed-USD allocation check ──────────────────────────────────────────
	if newPos.AllocatedCapital.IsPositive() {
		var used float64
		for _, b := range existing {
			if b.ID == excludeBotID {
				continue
			}
			used += b.Position.AllocatedCapital.InexactFloat64()
		}
		available := orch.Capital - used
		if newPos.AllocatedCapital.InexactFloat64() > available {
			return fmt.Errorf("insufficient capital: requesting %.2f but only %.2f available (%.2f total, %.2f allocated to other bots)",
				newPos.AllocatedCapital.InexactFloat64(), available, orch.Capital, used)
		}
	}

	// ── Percentage allocation check ─────────────────────────────────────────
	if newPos.AllocatedPct > 0 {
		var usedPct float64
		for _, b := range existing {
			if b.ID == excludeBotID {
				continue
			}
			usedPct += b.Position.AllocatedPct
		}
		available := 1.0 - usedPct
		if newPos.AllocatedPct > available {
			return fmt.Errorf("insufficient capital: requesting %.1f%% but only %.1f%% available (%.1f%% allocated to other bots)",
				newPos.AllocatedPct*100, available*100, usedPct*100)
		}
	}
	return nil
}

// create godoc
// @Summary Create bot
// @Tags bots
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body handler.CreateBotReq true "Bot create request"
// @Success 201 {object} shared.SuccessResponse[domain.BotSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 403 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots [post]
func (h *Handler) create(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	var req CreateBotReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	orchID, err := h.resolveOrchestratorID(userID, req.AccountID, req.OrchestratorID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	req.OrchestratorID = orchID
	cfg := req.ToDomain()
	orch, err := h.orchSvc.Get(cfg.OrchestratorID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "orchestrator not found")
		return
	}
	if orch.UserID != userID {
		shared.RespondWithError(c, http.StatusNotFound, "orchestrator not found")
		return
	}
	if !orch.Enabled {
		shared.RespondWithError(c, http.StatusForbidden, "orchestrator is disabled")
		return
	}
	if err := checkCapitalAllocation(orch, h.botMgr.ListByOrchestrator(orchID), cfg.Position, ""); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	instance, err := h.botMgr.Create(cfg)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusCreated, "Bot created successfully", instance.Summary())
}

// list godoc
// @Summary List bots
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param orchestrator_id query string false "Filter by orchestrator ID"
// @Param account_id query string false "Filter by account ID"
// @Success 200 {object} shared.SuccessResponse[[]domain.BotSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/bots [get]
func (h *Handler) list(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	if orchStr := c.Query("orchestrator_id"); orchStr != "" {
		orchID, err := uuid.Parse(orchStr)
		if err != nil {
			shared.RespondWithError(c, http.StatusBadRequest, "invalid orchestrator_id")
			return
		}
		if err := h.orchSvc.CheckOwner(orchID, userID); err != nil {
			shared.RespondWithError(c, http.StatusNotFound, "orchestrator not found")
			return
		}
		shared.RespondWithSuccess(c, http.StatusOK, "Bots retrieved successfully", h.botMgr.ListByOrchestrator(orchID))
		return
	}
	if accountStr := c.Query("account_id"); accountStr != "" {
		accountID, err := uuid.Parse(accountStr)
		if err != nil {
			shared.RespondWithError(c, http.StatusBadRequest, "invalid account_id")
			return
		}
		orch, err := h.orchSvc.GetByAccount(accountID)
		if err != nil || orch.UserID != userID {
			shared.RespondWithError(c, http.StatusNotFound, "orchestrator not found")
			return
		}
		shared.RespondWithSuccess(c, http.StatusOK, "Bots retrieved successfully", h.botMgr.ListByOrchestrator(orch.ID))
		return
	}
	// Without orchestrator_id filter, list all bots the user can access.
	orchs, err := h.orchSvc.ListByUser(userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	var bots []domain.BotSummary
	for _, o := range orchs {
		bots = append(bots, h.botMgr.ListByOrchestrator(o.ID)...)
	}
	if bots == nil {
		bots = []domain.BotSummary{}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bots retrieved successfully", bots)
}

// get godoc
// @Summary Get bot
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param id path string true "Bot ID"
// @Success 200 {object} shared.SuccessResponse[domain.BotSummary]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id} [get]
func (h *Handler) get(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := h.checkBotOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	bi, err := h.botMgr.Get(id)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bot retrieved successfully", bi.Summary())
}

// update godoc
// @Summary Update bot
// @Tags bots
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Bot ID"
// @Param request body handler.UpdateBotReq true "Bot update request"
// @Success 200 {object} shared.SuccessResponse[domain.BotSummary]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id} [put]
func (h *Handler) update(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	orchID, err := h.checkBotOwner(id, userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}

	var req UpdateBotReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	patch := req.ToDomain()

	// Validate capital allocation when sizing changes.
	if req.Position != nil && (req.Position.AllocatedCapital > 0 || req.Position.AllocatedPct > 0) {
		orch, err := h.orchSvc.Get(orchID)
		if err != nil {
			shared.RespondWithError(c, http.StatusNotFound, "orchestrator not found")
			return
		}
		if err := checkCapitalAllocation(orch, h.botMgr.ListByOrchestrator(orchID), patch.Position, id); err != nil {
			shared.RespondWithError(c, http.StatusBadRequest, err.Error())
			return
		}
	}

	if err := h.botMgr.Update(id, patch); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	bi, _ := h.botMgr.Get(id)
	shared.RespondWithSuccess(c, http.StatusOK, "Bot updated successfully", bi.Summary())
}

// delete godoc
// @Summary Delete bot
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param id path string true "Bot ID"
// @Success 200 {object} shared.SuccessResponse[BotActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 403 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id} [delete]
func (h *Handler) delete(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	orchID, err := h.checkBotOwner(id, userID)
	if err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	orch, err := h.orchSvc.Get(orchID)
	if err != nil || !orch.Enabled {
		shared.RespondWithError(c, http.StatusForbidden, "orchestrator is disabled")
		return
	}
	if err := h.botMgr.Delete(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bot deleted successfully", BotActionResp{Status: "deleted", ID: id})
}

// start godoc
// @Summary Start bot
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param id path string true "Bot ID"
// @Success 200 {object} shared.SuccessResponse[BotActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id}/start [post]
func (h *Handler) start(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := h.checkBotOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.botMgr.Start(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bot started successfully", BotActionResp{Status: "started", ID: id})
}

// stop godoc
// @Summary Stop bot
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param id path string true "Bot ID"
// @Success 200 {object} shared.SuccessResponse[BotActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id}/stop [post]
func (h *Handler) stop(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := h.checkBotOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.botMgr.Stop(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bot stopped successfully", BotActionResp{Status: "stopped", ID: id})
}

// restart godoc
// @Summary Restart bot — stop then start (re-registers with signal herald)
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param id path string true "Bot ID"
// @Success 200 {object} shared.SuccessResponse[BotActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id}/restart [post]
func (h *Handler) restart(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := h.checkBotOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.botMgr.Restart(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bot restarted successfully", BotActionResp{Status: "running", ID: id})
}

// pause godoc
// @Summary Pause bot
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param id path string true "Bot ID"
// @Success 200 {object} shared.SuccessResponse[BotActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id}/pause [post]
func (h *Handler) pause(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := h.checkBotOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.botMgr.Pause(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bot paused successfully", BotActionResp{Status: "paused", ID: id})
}

// resume godoc
// @Summary Resume bot
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param id path string true "Bot ID"
// @Success 200 {object} shared.SuccessResponse[BotActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id}/resume [post]
func (h *Handler) resume(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := h.checkBotOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.botMgr.Resume(id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bot resumed successfully", BotActionResp{Status: "running", ID: id})
}

// kill godoc
// @Summary Kill bot — stop and flatten all positions
// @Tags bots
// @Security BearerAuth
// @Produce json
// @Param id path string true "Bot ID"
// @Success 200 {object} shared.SuccessResponse[BotActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/bots/{id}/kill [post]
func (h *Handler) kill(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	id := c.Param("id")
	if _, err := h.checkBotOwner(id, userID); err != nil {
		shared.RespondWithError(c, http.StatusNotFound, "not found")
		return
	}
	if err := h.botMgr.Kill(c.Request.Context(), id); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Bot killed successfully", BotActionResp{Status: "stopped", ID: id})
}

// configureStrategy godoc
// @Summary Configure signal engine strategy
// @Tags signal-engine
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param request body handler.ConfigureStrategyReq true "Signal engine strategy config"
// @Success 200 {object} shared.SuccessResponse[ConfigureStrategyResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/signal-engine/configure [post]
func (h *Handler) configureStrategy(c *gin.Context) {
	var req ConfigureStrategyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	cfg := engine.ConfigMsg{Strategy: req.Strategy, Params: req.Params}
	if err := h.sigCli.Configure(c.Request.Context(), &cfg); err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Signal engine configured successfully", ConfigureStrategyResp{Status: "configured", Strategy: req.Strategy})
}

// Metrics writes Prometheus-style text metrics for all runtimes.
// Metrics godoc
// @Summary Prometheus metrics
// @Tags system
// @Produce plain
// @Success 200 {string} string
// @Router /metrics [get]
func (h *Handler) Metrics(c *gin.Context) {
	runtimes := h.reg.All()
	c.Header("Content-Type", "text/plain; version=0.0.4")

	var totalEquity, totalCash decimal.Decimal
	for _, rt := range runtimes {
		s := rt.Portfolio.Summary()
		totalEquity = totalEquity.Add(s.Equity)
		totalCash = totalCash.Add(s.Cash)
	}

	out := formatMetric("orchestrator_total_equity", totalEquity.InexactFloat64()) +
		formatMetric("orchestrator_total_cash", totalCash.InexactFloat64()) +
		formatMetric("orchestrator_running_bots", float64(len(h.botMgr.RunningBots()))) +
		formatMetric("orchestrator_active_runtimes", float64(len(runtimes)))
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
