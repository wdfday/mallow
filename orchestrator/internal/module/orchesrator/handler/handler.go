package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	pkgmw "mallow/pkg/middleware"
	"orchestrator/internal/module/orchesrator/service"
	botservice "orchestrator/internal/module/worker/service"
	"orchestrator/internal/runtime"
	"orchestrator/internal/shared"
)

type Handler struct {
	svc    *service.Service
	botMgr *botservice.Service
	reg    *runtime.Registry
}

func New(svc *service.Service, botMgr *botservice.Service, reg *runtime.Registry) *Handler {
	return &Handler{svc: svc, botMgr: botMgr, reg: reg}
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
	o := rg.Group("/orchestrators")
	{
		o.GET("", h.list)
		o.GET("/:id", h.get)
		o.PUT("/:id", h.update)
		o.POST("/:id/enable", h.enable)
		o.POST("/:id/disable", h.disable)
		o.POST("/:id/pause", h.pause)
		o.POST("/:id/resume", h.resume)
		o.GET("/:id/portfolio", h.portfolio)
		o.GET("/:id/positions", h.positions)
		o.GET("/:id/trades", h.trades)
		o.GET("/:id/orders", h.orders)

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
// @Summary Enable orchestrator
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/enable [post]
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
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator enabled successfully", ActionResp{Status: "enabled", ID: id})
}

// disable godoc
// @Summary Disable orchestrator
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/disable [post]
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
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator disabled successfully", ActionResp{Status: "disabled", ID: id})
}

// list godoc
// @Summary List orchestrators
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Success 200 {object} shared.SuccessResponse[[]OrchestratorResp]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/orchestrators [get]
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
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrators retrieved successfully", orchListToResp(cfgs))
}

// get godoc
// @Summary Get orchestrator
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[OrchestratorDetailResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id} [get]
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

	bots := h.botMgr.ListByOrchestrator(id)
	rt, rtErr := h.reg.Get(id)
	paused := false
	var lastSyncAt *time.Time
	if rtErr == nil {
		paused = rt.IsPaused()
		if t := rt.LastSyncAt(); !t.IsZero() {
			lastSyncAt = &t
		}
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator retrieved successfully", OrchestratorDetailResp{
		OrchestratorResp: orchToResp(cfg),
		Bots:             BotSummariesToResp(bots),
		Running:          rtErr == nil,
		Paused:           paused,
		LastSyncAt:       lastSyncAt,
	})
}

// update godoc
// @Summary Update orchestrator
// @Tags orchestrators
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Param request body handler.UpdateOrchestratorReq true "Orchestrator update"
// @Success 200 {object} shared.SuccessResponse[OrchestratorResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id} [put]
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
	var req UpdateOrchestratorReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	updateReq := service.UpdateReq{
		Name:    req.Name,
		Capital: req.Capital,
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
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator updated successfully", orchToResp(updated))
}

// pause godoc
// @Summary Pause orchestrator
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/pause [post]
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
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator paused successfully", ActionResp{Status: "paused", ID: id})
}

// resume godoc
// @Summary Resume orchestrator
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[ActionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/resume [post]
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
	shared.RespondWithSuccess(c, http.StatusOK, "Orchestrator resumed successfully", ActionResp{Status: "resumed", ID: id})
}

// --- Per-orchestrator runtime data ---

func (h *Handler) requireRuntime(c *gin.Context) *runtime.OrchestratorRuntime {
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

// portfolio godoc
// @Summary Get orchestrator portfolio summary
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[PortfolioResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/portfolio [get]
func (h *Handler) portfolio(c *gin.Context) {
	rt := h.requireRuntime(c)
	if rt == nil {
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Portfolio retrieved successfully", PortfolioToResp(rt.Portfolio.Summary()))
}

// positions godoc
// @Summary List orchestrator positions
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[[]PositionResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/positions [get]
func (h *Handler) positions(c *gin.Context) {
	rt := h.requireRuntime(c)
	if rt == nil {
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Positions retrieved successfully", PositionsToResp(rt.Portfolio.Positions()))
}

// trades godoc
// @Summary List orchestrator trades
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[[]TradeResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/trades [get]
func (h *Handler) trades(c *gin.Context) {
	rt := h.requireRuntime(c)
	if rt == nil {
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Trades retrieved successfully", TradesToResp(rt.Portfolio.Trades()))
}

// orders godoc
// @Summary List orchestrator orders
// @Tags orchestrators
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[[]OrderResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/orders [get]
func (h *Handler) orders(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if _, rtErr := h.reg.Get(id); rtErr != nil {
		shared.RespondWithError(c, http.StatusNotFound, "orchestrator runtime not active")
		return
	}
	var allOrders []OrderResp
	for _, bs := range h.botMgr.ListByOrchestrator(id) {
		bi, getErr := h.botMgr.Get(bs.ID)
		if getErr == nil {
			for _, o := range bi.Bot.Orders() {
				allOrders = append(allOrders, OrderToResp(o))
			}
		}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Orders retrieved successfully", allOrders)
}
