package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mallow/helm/internal/module/broker/domain"
	"mallow/helm/internal/module/broker/dto"
	"mallow/helm/internal/module/broker/service"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

// BrokerConnectionHandler handles HTTP requests for broker connections.
type BrokerConnectionHandler struct {
	service service.BrokerConnectionService
	logger  *slog.Logger
}

func NewBrokerConnectionHandler(svc service.BrokerConnectionService, logger *slog.Logger) *BrokerConnectionHandler {
	return &BrokerConnectionHandler{service: svc, logger: logger.With("component", "broker.handler")}
}

// RegisterRoutes registers all broker connection routes.
// Call this after wiring into the helm router.
func (h *BrokerConnectionHandler) RegisterRoutes(router *gin.Engine) {
	g := router.Group("/api/v1/broker-connections")
	g.GET("/providers", h.ListProviders)

	protected := router.Group("/api/v1/broker-connections")
	protected.Use(pkgmw.TrustedHeaders())
	{
		protected.POST("/okx", h.CreateOKX)
		protected.POST("/binance", h.CreateBinance)
		protected.POST("/alpaca", h.CreateAlpaca)
		protected.POST("/bybit", h.CreateBybit)

		protected.GET("", h.List)
		protected.GET("/:id", h.GetByID)
		protected.PUT("/:id", h.Update)
		protected.DELETE("/:id", h.Delete)
		protected.POST("/:id/activate", h.Activate)
		protected.POST("/:id/deactivate", h.Deactivate)
		protected.POST("/:id/test", h.TestConnection)
		protected.POST("/:id/rotate-key", h.RotateKey)
	}
}

// ListProviders returns supported broker providers and required credential fields.
func (h *BrokerConnectionHandler) ListProviders(c *gin.Context) {
	shared.RespondWithSuccess(c, http.StatusOK, "Broker providers retrieved successfully", dto.GetBrokerProviders())
}

// create is a DRY helper for all Create* handlers.
func (h *BrokerConnectionHandler) create(c *gin.Context, req *dto.CreateBrokerConnectionServiceRequest, brokerName string) {
	conn, err := h.service.Create(c.Request.Context(), req)
	if err != nil {
		h.logger.Error("Failed to create broker connection", "broker", brokerName, "error", err)
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusCreated, brokerName+" broker connection created successfully", dto.ToBrokerConnectionResponse(conn))
}

func (h *BrokerConnectionHandler) CreateOKX(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	var req dto.CreateOKXConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.create(c, req.ToServiceRequest(userID), "OKX")
}

func (h *BrokerConnectionHandler) CreateBinance(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	var req dto.CreateBinanceConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.create(c, req.ToServiceRequest(userID), "Binance")
}

func (h *BrokerConnectionHandler) CreateAlpaca(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	var req dto.CreateAlpacaConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.create(c, req.ToServiceRequest(userID), "Alpaca")
}

func (h *BrokerConnectionHandler) CreateBybit(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	var req dto.CreateBybitConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.create(c, req.ToServiceRequest(userID), "Bybit")
}

func (h *BrokerConnectionHandler) List(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	var query dto.ListBrokerConnectionsQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	filters := &service.ListFilters{
		ActiveOnly: query.ActiveOnly,
	}
	if query.BrokerType != nil {
		bt := domain.BrokerType(*query.BrokerType)
		filters.BrokerType = &bt
	}
	if query.Status != nil {
		st := domain.BrokerConnectionStatus(*query.Status)
		filters.Status = &st
	}
	connections, err := h.service.List(c.Request.Context(), userID, filters)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Broker connections retrieved successfully", dto.ToBrokerConnectionListResponse(connections))
}

func (h *BrokerConnectionHandler) GetByID(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid connection ID")
		return
	}
	conn, err := h.service.GetByID(c.Request.Context(), id, userID)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Broker connection retrieved successfully", dto.ToBrokerConnectionResponse(conn))
}

func (h *BrokerConnectionHandler) Update(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid connection ID")
		return
	}
	var req dto.UpdateBrokerConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	serviceReq := &service.UpdateBrokerConnectionRequest{
		BrokerName: req.BrokerName,
		APIKey:     req.APIKey,
		APISecret:  req.APISecret,
		Passphrase: req.Passphrase,
		Notes:      req.Notes,
	}
	conn, err := h.service.Update(c.Request.Context(), id, userID, serviceReq)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Broker connection updated successfully", dto.ToBrokerConnectionResponse(conn))
}

func (h *BrokerConnectionHandler) Delete(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid connection ID")
		return
	}
	if err := h.service.Delete(c.Request.Context(), id, userID); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithNoContent(c)
}

func (h *BrokerConnectionHandler) Activate(c *gin.Context) {
	h.simpleAction(c, h.service.Activate, "Broker connection activated")
}

func (h *BrokerConnectionHandler) Deactivate(c *gin.Context) {
	h.simpleAction(c, h.service.Deactivate, "Broker connection deactivated")
}

func (h *BrokerConnectionHandler) simpleAction(c *gin.Context, fn func(ctx context.Context, id, userID uuid.UUID) error, msg string) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid connection ID")
		return
	}
	if err := fn(c.Request.Context(), id, userID); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccessNoData(c, http.StatusOK, msg)
}

func (h *BrokerConnectionHandler) TestConnection(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid connection ID")
		return
	}
	if err := h.service.TestConnection(c.Request.Context(), id, userID); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccessNoData(c, http.StatusOK, "Connection test successful")
}

func (h *BrokerConnectionHandler) RotateKey(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid connection ID")
		return
	}
	var req dto.RotateKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	conn, err := h.service.RotateKey(c.Request.Context(), id, userID, &service.RotateKeyRequest{
		APIKey:     req.APIKey,
		APISecret:  req.APISecret,
		Passphrase: req.Passphrase,
	})
	if err != nil {
		h.logger.Error("Failed to rotate broker key", "conn_id", id, "error", err)
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "API key rotated successfully", dto.ToBrokerConnectionResponse(conn))
}
