package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	dto "mallow/helm/internal/module/helm/dto"
	"mallow/helm/internal/shared"
)

// ── Exchange probe endpoints ──────────────────────────────────────────────────
// These endpoints hit the live exchange directly (bypassing bots/portfolio).
// Intended for connectivity testing and manual order management.

// exchangeAccount godoc
// @Summary Get live exchange account snapshot
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[dto.ExchangeAccountResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/exchange/account [get]
func (h *Handler) exchangeAccount(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	syncer, ok := rt.Exchange.(exchange.AccountSyncer)
	if !ok {
		shared.RespondWithError(c, http.StatusBadRequest, "exchange does not support account sync")
		return
	}
	snap, err := syncer.SyncAccount(c.Request.Context(), rt.Creds, nil)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadGateway, err.Error())
		return
	}
	positions := make([]dto.ExchangePositionResp, len(snap.Positions))
	for i, p := range snap.Positions {
		positions[i] = dto.ExchangePositionResp{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
			CurPrice: p.CurPrice,
		}
	}
	balances := make([]dto.AssetBalanceResp, len(snap.Balances))
	for i, b := range snap.Balances {
		balances[i] = dto.AssetBalanceResp{Asset: b.Asset, Free: b.Free}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Account retrieved successfully", dto.ExchangeAccountResp{
		Cash:      snap.Cash,
		Equity:    snap.Equity,
		Positions: positions,
		Balances:  balances,
	})
}

// exchangePrice godoc
// @Summary Get live price for a symbol
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Param symbol query string true "Symbol (e.g. BTCUSDT)"
// @Success 200 {object} shared.SuccessResponse[dto.ExchangePriceResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/exchange/price [get]
func (h *Handler) exchangePrice(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	symbol := c.Query("symbol")
	if symbol == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "symbol is required")
		return
	}
	pf, ok := rt.Exchange.(exchange.PriceFetcher)
	if !ok {
		shared.RespondWithError(c, http.StatusBadRequest, "exchange does not support price fetch")
		return
	}
	price, err := pf.GetCurrentPrice(c.Request.Context(), rt.Creds, symbol)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadGateway, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Price retrieved successfully", dto.ExchangePriceResp{
		Symbol: symbol,
		Price:  price,
	})
}

// exchangePlaceOrder godoc
// @Summary Place a spot order directly on the exchange
// @Tags exchange
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Param request body dto.PlaceExchangeOrderReq true "Order request"
// @Success 201 {object} shared.SuccessResponse[dto.ExchangeOrderResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/exchange/orders [post]
func (h *Handler) exchangePlaceOrder(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	var req dto.PlaceExchangeOrderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	orderType := exchange.Market
	if req.Type == "limit" {
		if req.Price <= 0 {
			shared.RespondWithError(c, http.StatusBadRequest, "price is required for limit orders")
			return
		}
		orderType = exchange.Limit
	}
	result, err := rt.Exchange.PlaceOrder(c.Request.Context(), rt.Creds, exchange.OrderRequest{
		Symbol: req.Symbol,
		Side:   exchange.OrderSide(req.Side),
		Type:   orderType,
		Qty:    decimal.NewFromFloat(req.Qty),
		Price:  decimal.NewFromFloat(req.Price),
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusBadGateway, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusCreated, "Order placed successfully", dto.MapOrderResult(result))
}

// exchangeGetOrder godoc
// @Summary Get order status from the exchange
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Param order_id query string true "Order ID (format: SYMBOL:numeric_id for Binance)"
// @Success 200 {object} shared.SuccessResponse[dto.ExchangeOrderResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/exchange/orders [get]
func (h *Handler) exchangeGetOrder(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	orderID := c.Query("order_id")
	if orderID == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "order_id is required")
		return
	}
	result, err := rt.Exchange.GetOrder(c.Request.Context(), rt.Creds, orderID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadGateway, err.Error())
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Order retrieved successfully", dto.MapOrderResult(result))
}

// exchangeMetrics godoc
// @Summary Get exchange instrumentation metrics
// @Description Returns HTTP latency/error stats (PlaceOrder, GetOrder, CancelOrder, SyncAccount, Ping)
// @Description and WebSocket stream health (event counts, idle time, reconnect count).
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Success 200 {object} shared.SuccessResponse[dto.ExchangeMetricsResp]
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/exchange/metrics [get]
func (h *Handler) exchangeMetrics(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	snap := rt.ExchangeSnapshot()
	if snap == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "exchange metrics unavailable")
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Exchange metrics retrieved", dto.MetricsSnapshotToResp(*snap))
}

// exchangePing godoc
// @Summary Ping the exchange and report latency
// @Description Measures HTTP round-trip latency (via a dummy GetOrder call) and returns
// @Description the current WebSocket stream idle time from the metrics snapshot.
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Helm ID"
// @Success 200 {object} shared.SuccessResponse[dto.ExchangePingResp]
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/exchange/ping [get]
func (h *Handler) exchangePing(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	metered, ok := rt.Exchange.(*exchange.MeteredExchange)
	if !ok {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "exchange ping unavailable")
		return
	}
	rtt := metered.Ping(c.Request.Context())
	snap := metered.Snapshot()
	shared.RespondWithSuccess(c, http.StatusOK, "Pong", dto.ExchangePingResp{
		Exchange:  snap.Name,
		LatencyMs: float64(rtt.Nanoseconds()) / 1e6,
		WSIdleSec: snap.WS.IdleSec,
	})
}

// exchangeCancelOrder godoc
// @Summary Cancel an open order on the exchange
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Param order_id query string true "Order ID (format: SYMBOL:numeric_id for Binance)"
// @Success 200 {object} shared.SuccessResponse[any]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/exchange/orders [delete]
func (h *Handler) exchangeCancelOrder(c *gin.Context) {
	userID, ok := callerUserID(c)
	if !ok {
		return
	}
	rt := h.requireOwnedRuntime(c, userID)
	if rt == nil {
		return
	}
	orderID := c.Query("order_id")
	if orderID == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "order_id is required")
		return
	}
	if err := rt.Exchange.CancelOrder(c.Request.Context(), rt.Creds, orderID); err != nil {
		shared.RespondWithError(c, http.StatusBadGateway, err.Error())
		return
	}
	shared.RespondWithSuccessNoData(c, http.StatusOK, "Order cancelled successfully")
}
