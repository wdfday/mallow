package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"

	"mallow/investment/internal/middleware"
	"mallow/investment/internal/module/watchlist/domain"
	"mallow/investment/internal/module/watchlist/dto"
	"mallow/investment/internal/module/watchlist/service"
	"mallow/investment/internal/shared"
)

type Handler struct {
	svc service.Service
}

func New(svc service.Service) *Handler {
	return &Handler{svc: svc}
}

// List godoc
// @Summary List watchlist items
// @Description Get the authenticated user's watchlist
// @Tags watchlist
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 200 {object} shared.SuccessResponse[[]dto.ItemResponse]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/watchlist [get]
func (h *Handler) List(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	items, err := h.svc.List(c.Request.Context(), user.ID)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	resp := make([]dto.ItemResponse, len(items))
	for i, item := range items {
		resp[i] = dto.ToItemResponse(item)
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Watchlist retrieved successfully", resp)
}

// Add godoc
// @Summary Add watchlist item
// @Description Add a symbol to the authenticated user's watchlist
// @Tags watchlist
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param request body dto.AddRequest true "Watchlist item"
// @Success 201 {object} shared.SuccessResponse[dto.ItemResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 409 {object} shared.ErrorResponse
// @Router /api/v1/investment/watchlist [post]
func (h *Handler) Add(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req dto.AddRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}

	var targetPrice *decimal.Decimal
	if req.TargetPrice != nil {
		d := decimal.NewFromFloat(*req.TargetPrice)
		targetPrice = &d
	}
	item := &domain.WatchlistItem{
		UserID:      user.ID,
		Symbol:      req.Symbol,
		Name:        req.Name,
		AssetType:   req.AssetType,
		TargetPrice: targetPrice,
		Notes:       req.Notes,
	}
	if err := h.svc.Add(c.Request.Context(), item); err != nil {
		shared.RespondWithError(c, http.StatusConflict, "symbol already in watchlist")
		return
	}
	shared.RespondWithSuccess(c, http.StatusCreated, "Watchlist item added successfully", dto.ToItemResponse(*item))
}

// Delete godoc
// @Summary Delete watchlist item
// @Description Remove a symbol from the authenticated user's watchlist
// @Tags watchlist
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param symbol path string true "Asset symbol"
// @Success 204 {string} string "No Content"
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/watchlist/{symbol} [delete]
func (h *Handler) Delete(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	symbol := c.Param("symbol")
	if err := h.svc.Delete(c.Request.Context(), user.ID, symbol); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithNoContent(c)
}
