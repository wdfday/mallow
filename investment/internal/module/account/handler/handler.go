package handler

import (
	"log/slog"
	"net/http"

	"mallow/investment/internal/middleware"
	accountdto "mallow/investment/internal/module/account/dto"
	accountservice "mallow/investment/internal/module/account/service"
	"mallow/investment/internal/shared"
	pkgmw "mallow/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// Handler manages account endpoints.
type Handler struct {
	service accountservice.Service
	logger  *slog.Logger
}

// NewHandler constructs an account handler.
func NewHandler(
	service accountservice.Service,
	logger *slog.Logger,
) *Handler {
	return &Handler{
		service: service,
		logger:  logger.With("component", "account.handler"),
	}
}

// RegisterRoutes wires account read routes under /api/v1/investment/accounts.
func (h *Handler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware) {
	accounts := r.Group("/api/v1/investment/accounts")
	accounts.Use(pkgmw.TrustedHeaders(), authMiddleware.AuthMiddleware())
	{
		accounts.GET("", h.getMyAccounts)
		accounts.GET("/:id", h.getAccount)
	}
}

// getMyAccounts godoc
// @Summary Get my accounts
// @Description Get all accounts for the authenticated user with optional filters
// @Tags accounts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param account_type query string false "Filter by account type" Enums(cash, bank, savings, credit_card, investment, crypto_wallet)
// @Param is_active query bool false "Filter by active status"
// @Param is_primary query bool false "Filter by primary status"
// @Param include_deleted query bool false "Include deleted accounts"
// @Success 200 {object} shared.SuccessResponse[accountdto.AccountsListResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/accounts [get]
func (h *Handler) getMyAccounts(c *gin.Context) {
	currentUser, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	var req accountdto.ListAccountsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid query parameters")
		return
	}

	accounts, total, err := h.service.GetByUserID(c.Request.Context(), currentUser.ID.String(), req)
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	items := make([]accountdto.AccountResponse, len(accounts))
	for i, acc := range accounts {
		items[i] = accountdto.ToResponse(acc)
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Accounts retrieved successfully", accountdto.AccountsListResponse{
		Items: items,
		Total: total,
	})
}

// getAccount godoc
// @Summary Get account by ID
// @Description Get a specific account by ID (must belong to authenticated user)
// @Tags accounts
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Account ID (UUID)"
// @Success 200 {object} shared.SuccessResponse[accountdto.AccountResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/investment/accounts/{id} [get]
func (h *Handler) getAccount(c *gin.Context) {
	currentUser, exists := middleware.GetCurrentUser(c)
	if !exists {
		shared.RespondWithError(c, http.StatusUnauthorized, "user not found in context")
		return
	}

	id := c.Param("id")
	if id == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "account id is required")
		return
	}

	account, err := h.service.GetByID(c.Request.Context(), id, currentUser.ID.String())
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Account retrieved successfully", accountdto.ToResponse(*account))
}
