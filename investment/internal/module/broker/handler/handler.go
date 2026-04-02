package handler

import (
	"context"
	"log/slog"
	"mallow/investment/internal/middleware"
	"mallow/investment/internal/module/broker/domain"
	"mallow/investment/internal/module/broker/dto"
	"mallow/investment/internal/module/broker/service"
	"mallow/investment/internal/shared"
	pkgmw "mallow/pkg/middleware"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
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
func (h *BrokerConnectionHandler) RegisterRoutes(router *gin.Engine, authMiddleware *middleware.Middleware) {
	g := router.Group("/api/v1/investment/broker-connections")
	g.GET("/providers", h.ListProviders)

	protected := router.Group("/api/v1/investment/broker-connections")
	protected.Use(pkgmw.TrustedHeaders(), authMiddleware.AuthMiddleware())
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
		protected.POST("/:id/refresh-token", h.RefreshToken)
		protected.POST("/:id/test", h.TestConnection)

		// ReBroker: change the broker linked to an account.
		// Body: {"account_id": "uuid"}
		protected.POST("/:id/rebroker", h.ReBroker)
	}
}

// ListProviders godoc
// @Summary List broker providers
// @Description Get supported broker providers and required credential fields
// @Tags broker-connections
// @Accept json
// @Produce json
// @Success 200 {object} shared.SuccessResponse[dto.ListBrokerProvidersResponse]
// @Router /api/v1/investment/broker-connections/providers [get]
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

// CreateOKX godoc
// @Summary Create OKX broker connection
// @Description Create a new OKX broker connection for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.CreateOKXConnectionRequest true "OKX connection payload"
// @Success 201 {object} shared.SuccessResponse[dto.BrokerConnectionResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/okx [post]
func (h *BrokerConnectionHandler) CreateOKX(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
		return
	}
	var req dto.CreateOKXConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.create(c, req.ToServiceRequest(userID), "OKX")
}

// CreateBinance godoc
// @Summary Create Binance broker connection
// @Description Create a new Binance broker connection for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.CreateBinanceConnectionRequest true "Binance connection payload"
// @Success 201 {object} shared.SuccessResponse[dto.BrokerConnectionResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/binance [post]
func (h *BrokerConnectionHandler) CreateBinance(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
		return
	}
	var req dto.CreateBinanceConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.create(c, req.ToServiceRequest(userID), "Binance")
}

// CreateAlpaca godoc
// @Summary Create Alpaca broker connection
// @Description Create a new Alpaca broker connection for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.CreateAlpacaConnectionRequest true "Alpaca connection payload"
// @Success 201 {object} shared.SuccessResponse[dto.BrokerConnectionResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/alpaca [post]
func (h *BrokerConnectionHandler) CreateAlpaca(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
		return
	}
	var req dto.CreateAlpacaConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.create(c, req.ToServiceRequest(userID), "Alpaca")
}

// CreateBybit godoc
// @Summary Create Bybit broker connection
// @Description Create a new Bybit broker connection for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.CreateBybitConnectionRequest true "Bybit connection payload"
// @Success 201 {object} shared.SuccessResponse[dto.BrokerConnectionResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/bybit [post]
func (h *BrokerConnectionHandler) CreateBybit(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
		return
	}
	var req dto.CreateBybitConnectionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	h.create(c, req.ToServiceRequest(userID), "Bybit")
}

// List godoc
// @Summary List broker connections
// @Description Get broker connections for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param broker_type query string false "Broker type"
// @Param status query string false "Connection status"
// @Param auto_sync_only query bool false "Only return auto-sync connections"
// @Param active_only query bool false "Only return active connections"
// @Param needing_sync_only query bool false "Only return connections needing sync"
// @Success 200 {object} shared.SuccessResponse[dto.BrokerConnectionListResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections [get]
func (h *BrokerConnectionHandler) List(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
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

// GetByID godoc
// @Summary Get broker connection
// @Description Get a broker connection by ID for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Success 200 {object} shared.SuccessResponse[dto.BrokerConnectionResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/{id} [get]
func (h *BrokerConnectionHandler) GetByID(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
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

// Update godoc
// @Summary Update broker connection
// @Description Update a broker connection for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Param request body dto.UpdateBrokerConnectionRequest true "Broker connection update payload"
// @Success 200 {object} shared.SuccessResponse[dto.BrokerConnectionResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/{id} [put]
func (h *BrokerConnectionHandler) Update(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
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

// Delete godoc
// @Summary Delete broker connection
// @Description Delete a broker connection for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Success 204 {string} string "No Content"
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/{id} [delete]
func (h *BrokerConnectionHandler) Delete(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
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

// Activate godoc
// @Summary Activate broker connection
// @Description Activate a broker connection for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/{id}/activate [post]
func (h *BrokerConnectionHandler) Activate(c *gin.Context) {
	h.simpleAction(c, h.service.Activate, "Broker connection activated")
}

// Deactivate godoc
// @Summary Deactivate broker connection
// @Description Deactivate a broker connection for the authenticated user
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/{id}/deactivate [post]
func (h *BrokerConnectionHandler) Deactivate(c *gin.Context) {
	h.simpleAction(c, h.service.Deactivate, "Broker connection deactivated")
}

func (h *BrokerConnectionHandler) simpleAction(c *gin.Context, fn func(ctx context.Context, id, userID uuid.UUID) error, msg string) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
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

// RefreshToken godoc
// @Summary Refresh broker token
// @Description Refresh token or credentials for a broker connection
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Success 200 {object} shared.SuccessResponse[dto.BrokerConnectionResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/{id}/refresh-token [post]
func (h *BrokerConnectionHandler) RefreshToken(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid connection ID")
		return
	}
	conn, err := h.service.RefreshToken(c.Request.Context(), id, userID)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Token refreshed successfully", dto.ToBrokerConnectionResponse(conn))
}

// TestConnection godoc
// @Summary Test broker connection
// @Description Verify that a broker connection can authenticate and respond
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Connection ID"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/{id}/test [post]
func (h *BrokerConnectionHandler) TestConnection(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
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

// ReBroker godoc
// @Summary Change broker for an account
// @Description Link an account to a different broker connection, restarting the orchestrator runtime
// @Tags broker-connections
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "New broker connection ID"
// @Param request body dto.ReBrokerRequest true "Account to rebroker"
// @Success 200 {object} shared.Success
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/broker-connections/{id}/rebroker [post]
func (h *BrokerConnectionHandler) ReBroker(c *gin.Context) {
	userID, err := getUserIDFromContext(c)
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, err.Error())
		return
	}
	newBrokerID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid broker connection ID")
		return
	}
	var req dto.ReBrokerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.service.ReBroker(c.Request.Context(), req.AccountID, newBrokerID, userID); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccessNoData(c, http.StatusOK, "Broker updated successfully")
}

func getUserIDFromContext(c *gin.Context) (uuid.UUID, error) {
	v, exists := c.Get("user_id")
	if !exists {
		return uuid.Nil, ErrUserIDNotFound
	}
	if id, ok := v.(uuid.UUID); ok {
		return id, nil
	}
	if s, ok := v.(string); ok {
		return uuid.Parse(s)
	}
	return uuid.Nil, ErrInvalidUserID
}
