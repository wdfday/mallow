package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	accountdto "mallow/helm/internal/module/account/dto"
	accountservice "mallow/helm/internal/module/account/service"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

// Handler manages account endpoints.
type Handler struct {
	service accountservice.Service
}

// NewHandler constructs an account handler.
func NewHandler(service accountservice.Service) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes wires account routes under /api/v1/accounts.
func (h *Handler) RegisterRoutes(r *gin.Engine) {
	accounts := r.Group("/api/v1/accounts")
	accounts.Use(pkgmw.TrustedHeaders())
	{
		accounts.GET("", h.getMyAccounts)
		accounts.GET("/:id", h.getAccount)
	}
}

// getMyAccounts godoc
// @Summary List my accounts
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param account_type query string false "Filter by account type"
// @Param is_active query bool false "Filter by active status"
// @Success 200 {object} shared.SuccessResponse[accountdto.AccountsListResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Router /api/v1/accounts [get]
func (h *Handler) getMyAccounts(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}

	var req accountdto.ListAccountsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid query parameters")
		return
	}

	accounts, total, err := h.service.GetByUserID(c.Request.Context(), userID.String(), req)
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
// @Tags accounts
// @Security BearerAuth
// @Produce json
// @Param id path string true "Account ID"
// @Success 200 {object} shared.SuccessResponse[accountdto.AccountResponse]
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id} [get]
func (h *Handler) getAccount(c *gin.Context) {
	userID, ok := shared.CallerUserID(c)
	if !ok {
		return
	}

	id := c.Param("id")
	if id == "" {
		shared.RespondWithError(c, http.StatusBadRequest, "account id is required")
		return
	}

	account, err := h.service.GetByID(c.Request.Context(), id, userID.String())
	if err != nil {
		shared.HandleError(c, err)
		return
	}

	shared.RespondWithSuccess(c, http.StatusOK, "Account retrieved successfully", accountdto.ToResponse(*account))
}
