package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

// resolveAccount verifies ownership and returns the parsed accountID.
func (h *Handler) resolveAccount(c *gin.Context) (uuid.UUID, bool) {
	userID, err := uuid.Parse(pkgmw.UserID(c))
	if err != nil {
		shared.RespondWithError(c, http.StatusUnauthorized, "invalid user")
		return uuid.Nil, false
	}
	accountID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid account id")
		return uuid.Nil, false
	}
	if _, sErr := h.service.GetByID(c.Request.Context(), accountID.String(), userID.String()); sErr != nil {
		shared.RespondWithError(c, http.StatusNotFound, "account not found")
		return uuid.Nil, false
	}
	return accountID, true
}

// resolveAccountHelm verifies ownership, looks up the helm, and returns both IDs.
func (h *Handler) resolveAccountHelm(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	accountID, ok := h.resolveAccount(c)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	helm, sErr := h.helmSvc.GetByAccount(accountID)
	if sErr != nil {
		shared.RespondWithError(c, http.StatusNotFound, "no helm for this account")
		return uuid.Nil, uuid.Nil, false
	}
	return accountID, helm.ID, true
}
