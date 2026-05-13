package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	dto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/module/helm/service"
	"mallow/helm/internal/runtime"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

type Handler struct {
	svc     *service.Service
	handMgr HandManager
	reg     *runtime.Registry
}

func New(svc *service.Service, handMgr HandManager, reg *runtime.Registry) *Handler {
	return &Handler{svc: svc, handMgr: handMgr, reg: reg}
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
// @Summary List orchestrator trades
// @Tags helms
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[[]dto.TradeResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/trades [get]
func (h *Handler) trades(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Trades retrieved successfully", dto.TradesToResp(rt.Portfolio.Trades()))
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
