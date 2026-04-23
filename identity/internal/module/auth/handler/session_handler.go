package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"mallow/identity/internal/middleware"
	"mallow/identity/internal/module/auth/domain"
	"mallow/identity/internal/module/auth/service"
	"mallow/identity/internal/shared"
)

// SessionHandler serves session management endpoints.
type SessionHandler struct {
	sessionService service.ISessionService
}

// NewSessionHandler constructs a SessionHandler.
func NewSessionHandler(sessionService service.ISessionService) *SessionHandler {
	return &SessionHandler{sessionService: sessionService}
}

// RegisterRoutes wires session routes — all require authentication.
func (h *SessionHandler) RegisterRoutes(r *gin.Engine, authMiddleware *middleware.Middleware) {
	g := r.Group("/api/v1/auth/sessions")
	g.Use(authMiddleware.AuthMiddleware())
	{
		g.GET("", h.list)
		g.DELETE("/:sid", h.revoke)
		g.DELETE("", h.revokeAll)
	}
}

// list godoc
// @Summary List active sessions
// @Description Returns all active sessions for the authenticated user
// @Tags auth
// @Produce json
// @Success 200 {array} sessionResponse
// @Router /api/v1/auth/sessions [get]
// @Security BearerAuth
func (h *SessionHandler) list(c *gin.Context) {
	userID := c.GetString("user_id")
	sessions, err := h.sessionService.ListSessions(c.Request.Context(), userID)
	if err != nil {
		shared.HandleError(c, err)
		return
	}
	currentSID := c.GetString("session_id")
	shared.RespondWithSuccess(c, http.StatusOK, "ok", toSessionResponses(sessions, currentSID))
}

// revoke godoc
// @Summary Revoke a session
// @Description Revokes the specified session, immediately invalidating its tokens
// @Tags auth
// @Produce json
// @Param sid path string true "Session ID"
// @Success 200 {object} shared.SuccessResponse
// @Router /api/v1/auth/sessions/{sid} [delete]
// @Security BearerAuth
func (h *SessionHandler) revoke(c *gin.Context) {
	sid := c.Param("sid")
	userID := c.GetString("user_id")
	if err := h.sessionService.RevokeSession(c.Request.Context(), sid, userID); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccessNoData(c, http.StatusOK, "session revoked")
}

// revokeAll godoc
// @Summary Revoke all sessions
// @Description Logs out from all devices by revoking every active session
// @Tags auth
// @Produce json
// @Success 200 {object} shared.SuccessResponse
// @Router /api/v1/auth/sessions [delete]
// @Security BearerAuth
func (h *SessionHandler) revokeAll(c *gin.Context) {
	userID := c.GetString("user_id")
	if err := h.sessionService.RevokeAllSessions(c.Request.Context(), userID); err != nil {
		shared.HandleError(c, err)
		return
	}
	shared.RespondWithSuccessNoData(c, http.StatusOK, "all sessions revoked")
}

type sessionResponse struct {
	SID       string    `json:"sid"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	IsCurrent bool      `json:"is_current"`
}

func toSessionResponses(sessions []domain.Session, currentSID string) []sessionResponse {
	result := make([]sessionResponse, len(sessions))
	for i, s := range sessions {
		result[i] = sessionResponse{
			SID:       s.SID,
			IP:        s.IP,
			UserAgent: s.UserAgent,
			CreatedAt: s.CreatedAt,
			ExpiresAt: s.ExpiresAt,
			IsCurrent: s.SID == currentSID,
		}
	}
	return result
}
