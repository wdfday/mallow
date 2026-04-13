package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"orchestrator/internal/infra/exchange"
	"orchestrator/internal/shared"
)

// ── Exchange probe endpoints ──────────────────────────────────────────────────
// These endpoints hit the live exchange directly (bypassing bots/portfolio).
// Intended for connectivity testing and manual order management.

// ExchangeAccountResp is a flattened account snapshot.
type ExchangeAccountResp struct {
	Cash      decimal.Decimal        `json:"cash"`
	Equity    decimal.Decimal        `json:"equity"`
	Positions []ExchangePositionResp `json:"positions"`
}

type ExchangePositionResp struct {
	Symbol   string          `json:"symbol"`
	Qty      decimal.Decimal `json:"qty"`
	AvgPrice decimal.Decimal `json:"avg_price"`
	CurPrice decimal.Decimal `json:"cur_price"`
}

type ExchangePriceResp struct {
	Symbol string          `json:"symbol"`
	Price  decimal.Decimal `json:"price"`
}

type PlaceExchangeOrderReq struct {
	Symbol string  `json:"symbol" binding:"required"`
	Side   string  `json:"side"   binding:"required,oneof=buy sell"`
	Type   string  `json:"type"   binding:"required,oneof=market limit"`
	Qty    float64 `json:"qty"   binding:"required,gt=0"`
	Price  float64 `json:"price"`
}

type ExchangeOrderResp struct {
	ID        string          `json:"id"`
	Symbol    string          `json:"symbol"`
	Side      string          `json:"side"`
	Status    string          `json:"status"`
	Qty       decimal.Decimal `json:"qty"`
	FilledQty decimal.Decimal `json:"filled_qty"`
	FilledAvg decimal.Decimal `json:"filled_avg_price"`
}

func mapOrderResult(r *exchange.OrderResult) ExchangeOrderResp {
	return ExchangeOrderResp{
		ID:        r.ID,
		Symbol:    r.Symbol,
		Side:      string(r.Side),
		Status:    r.Status,
		Qty:       r.Qty,
		FilledQty: r.FilledQty,
		FilledAvg: r.FilledAvg,
	}
}

// exchangeAccount godoc
// @Summary Get live exchange account snapshot
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Success 200 {object} shared.SuccessResponse[ExchangeAccountResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/exchange/account [get]
func (h *Handler) exchangeAccount(c *gin.Context) {
	rt := h.requireRuntime(c)
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
	positions := make([]ExchangePositionResp, len(snap.Positions))
	for i, p := range snap.Positions {
		positions[i] = ExchangePositionResp{
			Symbol:   p.Symbol,
			Qty:      p.Qty,
			AvgPrice: p.AvgPrice,
			CurPrice: p.CurPrice,
		}
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Account retrieved successfully", ExchangeAccountResp{
		Cash:      snap.Cash,
		Equity:    snap.Equity,
		Positions: positions,
	})
}

// exchangePrice godoc
// @Summary Get live price for a symbol
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Param symbol query string true "Symbol (e.g. BTCUSDT)"
// @Success 200 {object} shared.SuccessResponse[ExchangePriceResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/exchange/price [get]
func (h *Handler) exchangePrice(c *gin.Context) {
	rt := h.requireRuntime(c)
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
	shared.RespondWithSuccess(c, http.StatusOK, "Price retrieved successfully", ExchangePriceResp{
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
// @Param request body handler.PlaceExchangeOrderReq true "Order request"
// @Success 201 {object} shared.SuccessResponse[ExchangeOrderResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/exchange/orders [post]
func (h *Handler) exchangePlaceOrder(c *gin.Context) {
	rt := h.requireRuntime(c)
	if rt == nil {
		return
	}
	var req PlaceExchangeOrderReq
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
	shared.RespondWithSuccess(c, http.StatusCreated, "Order placed successfully", mapOrderResult(result))
}

// exchangeGetOrder godoc
// @Summary Get order status from the exchange
// @Tags exchange
// @Security BearerAuth
// @Produce json
// @Param id path string true "Orchestrator ID"
// @Param order_id query string true "Order ID (format: SYMBOL:numeric_id for Binance)"
// @Success 200 {object} shared.SuccessResponse[ExchangeOrderResp]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 502 {object} shared.ErrorResponse
// @Router /api/orchestrators/{id}/exchange/orders [get]
func (h *Handler) exchangeGetOrder(c *gin.Context) {
	rt := h.requireRuntime(c)
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
	shared.RespondWithSuccess(c, http.StatusOK, "Order retrieved successfully", mapOrderResult(result))
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
// @Router /api/orchestrators/{id}/exchange/orders [delete]
func (h *Handler) exchangeCancelOrder(c *gin.Context) {
	rt := h.requireRuntime(c)
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
