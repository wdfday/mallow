package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/investment/internal/middleware"
	"mallow/investment/internal/module/portfolio/event"
	txdomain "mallow/investment/internal/module/transaction/domain"
	"mallow/investment/internal/module/transaction/repository"
	"mallow/investment/internal/module/transaction/service"
	"mallow/investment/internal/shared"
)

type Handler struct {
	svc service.Service
}

func New(svc service.Service) *Handler {
	return &Handler{svc: svc}
}

// List godoc
// @Summary List portfolio transactions
// @Description Get transaction history for the authenticated user
// @Tags portfolio
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param type query string false "Transaction type"
// @Param symbol query string false "Asset symbol"
// @Param limit query int false "Max records to return (default 50)"
// @Param offset query int false "Records to skip (default 0)"
// @Success 200 {object} shared.SuccessResponse[[]txdomain.PortfolioTransaction]
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/transactions [get]
func (h *Handler) List(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit, offset := parsePagination(c, 50)
	filter := repository.ListFilter{
		TxType: c.Query("type"),
		Symbol: c.Query("symbol"),
		Limit:  limit,
		Offset: offset,
	}
	var txs []txdomain.PortfolioTransaction
	txs, err := h.svc.List(c.Request.Context(), user.ID, filter)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccess(c, http.StatusOK, "Transactions retrieved successfully", txs)
}

// Create godoc
// @Summary Record a manual transaction
// @Description Record a manual portfolio transaction for the authenticated user
// @Tags portfolio
// @Accept json
// @Produce json
// @Security BearerAuth
// @Success 201 {object} shared.SuccessResponse[any]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/transactions [post]
func (h *Handler) Create(c *gin.Context) {
	user, ok := middleware.GetCurrentUser(c)
	if !ok {
		shared.RespondWithError(c, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req struct {
		AccountID       string                `json:"account_id" binding:"required"`
		Type            event.TransactionType `json:"type" binding:"required"`
		Symbol          string                `json:"symbol"`
		Name            string                `json:"name"`
		AssetType       string                `json:"asset_type"`
		Exchange        string                `json:"exchange"`
		Currency        string                `json:"currency"`
		Quantity        float64               `json:"quantity"`
		PricePerUnit    float64               `json:"price_per_unit"`
		Amount          float64               `json:"amount" binding:"required"`
		Fees            float64               `json:"fees"`
		Commission      float64               `json:"commission"`
		Tax             float64               `json:"tax"`
		TransactionDate time.Time             `json:"transaction_date"`
		Notes           string                `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, err.Error())
		return
	}
	if req.TransactionDate.IsZero() {
		req.TransactionDate = time.Now().UTC()
	}
	if req.Currency == "" {
		req.Currency = "USD"
	}

	accountID, err := uuid.Parse(req.AccountID)
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid account_id")
		return
	}

	tx := event.TransactionRecorded{
		UserID:          user.ID,
		Type:            req.Type,
		Symbol:          req.Symbol,
		Name:            req.Name,
		AssetType:       req.AssetType,
		Exchange:        req.Exchange,
		Currency:        req.Currency,
		Quantity:        decimal.NewFromFloat(req.Quantity),
		PricePerUnit:    decimal.NewFromFloat(req.PricePerUnit),
		Amount:          decimal.NewFromFloat(req.Amount),
		Fees:            decimal.NewFromFloat(req.Fees),
		Commission:      decimal.NewFromFloat(req.Commission),
		Tax:             decimal.NewFromFloat(req.Tax),
		TransactionDate: req.TransactionDate,
		Source:          "manual",
		Notes:           req.Notes,
	}

	if err := h.svc.Record(c.Request.Context(), accountID, user.ID, tx); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccessNoData(c, http.StatusCreated, "Transaction recorded successfully")
}

// parsePagination reads limit/offset query params with sensible defaults.
func parsePagination(c *gin.Context, defaultLimit int) (limit, offset int) {
	limit = defaultLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return
}
