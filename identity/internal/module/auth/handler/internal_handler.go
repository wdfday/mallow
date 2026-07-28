package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"mallow/identity/internal/middleware"
	"mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/shared"
)

// InternalHandler exposes service-to-service endpoints protected by X-Service-Secret.
type InternalHandler struct {
	blacklistRepo domain.ITokenBlacklistRepository
}

// NewInternalHandler constructs an InternalHandler.
func NewInternalHandler(blacklistRepo domain.ITokenBlacklistRepository) *InternalHandler {
	return &InternalHandler{blacklistRepo: blacklistRepo}
}

// RegisterRoutes registers internal service-to-service routes.
func (h *InternalHandler) RegisterRoutes(r *gin.Engine, serviceSecret string) {
	internal := r.Group("/api/v1/internal")
	internal.Use(middleware.ServiceAuth(serviceSecret))
	{
		internal.GET("/blacklist/check", h.checkRevoked)
	}
}

// checkRevoked godoc
// @Summary Check if a session or user is revoked (internal)
// @Description Checks blacklist by sid and sub; falls back to DB when Redis is cold (wakes cache).
// @Tags internal
// @Produce json
// @Param sid query string false "Session ID"
// @Param sub query string false "User ID (subject)"
// @Success 200 {object} map[string]bool
// @Failure 500 {object} shared.ErrorResponse
// @Router /api/v1/internal/blacklist/check [get]
func (h *InternalHandler) checkRevoked(c *gin.Context) {
	sid := c.Query("sid")
	sub := c.Query("sub")

	blacklisted, err := h.blacklistRepo.IsRevokedByFields(c.Request.Context(), sid, sub)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	// Flat {"blacklisted": bool} shape — NOT the standard RespondWithSuccess envelope.
	// api-gateway's IdentityClient.CheckRevoked decodes this field at the top level
	// (api-gateway/internal/service/identity.go); wrapping it under "data" would silently
	// break that cross-service contract.
	c.JSON(http.StatusOK, gin.H{"blacklisted": blacklisted})
}
