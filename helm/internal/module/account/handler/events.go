package handler

import (
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/shared"
	pkgmw "mallow/pkg/middleware"
)

// sseStream is a generic helper that subscribes to a NATS subject and forwards
// messages to the client as SSE data frames until the request is cancelled.
func (h *Handler) sseStream(c *gin.Context, subject string) {
	if h.nc == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "event stream not available")
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ch := make(chan []byte, 32)

	sub, err := h.nc.Subscribe(subject, func(msg *nats.Msg) {
		select {
		case ch <- msg.Data:
		default:
		}
	})
	if err != nil {
		shared.RespondWithError(c, http.StatusInternalServerError, "failed to subscribe to event stream")
		return
	}
	defer sub.Unsubscribe() //nolint:errcheck

	c.Stream(func(w io.Writer) bool {
		select {
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data) //nolint:errcheck
			return true
		case <-c.Request.Context().Done():
			return false
		}
	})
}

// events godoc
// @Summary Stream real-time helm events for an account via SSE
// @Description Subscribes to helm.events.{helmID} and streams behavioral events (signals, orders, fills, lifecycle).
// @Tags accounts
// @Security BearerAuth
// @Produce text/event-stream
// @Param id path string true "Account ID"
// @Success 200 {string} string "SSE stream"
// @Failure 400 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/events [get]
func (h *Handler) events(c *gin.Context) {
	accountID, helmID, ok := h.resolveAccountHelm(c)
	if !ok {
		return
	}
	_ = accountID
	h.sseStream(c, fmt.Sprintf(natsapi.SubjHelmEvents, helmID))
}

// streamFills godoc
// @Summary Stream real-time order fills for an account via SSE
// @Description Each SSE data frame is a JSON-encoded TransactionMsg published to trade.filled.{accountID}.
// @Tags accounts
// @Security BearerAuth
// @Produce text/event-stream
// @Param id path string true "Account ID"
// @Success 200 {string} string "SSE stream"
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/stream/fills [get]
func (h *Handler) streamFills(c *gin.Context) {
	accountID, ok := h.resolveAccount(c)
	if !ok {
		return
	}
	h.sseStream(c, fmt.Sprintf(natsapi.SubjTradeFilled, accountID))
}

// streamPortfolio godoc
// @Summary Stream real-time portfolio sync events for an account via SSE
// @Description Each SSE data frame is a JSON-encoded PortfolioSyncEvent published to portfolio.synced.{accountID}.
// @Tags accounts
// @Security BearerAuth
// @Produce text/event-stream
// @Param id path string true "Account ID"
// @Success 200 {string} string "SSE stream"
// @Failure 400 {object} shared.ErrorResponse
// @Failure 401 {object} shared.ErrorResponse
// @Failure 404 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/accounts/{id}/stream/portfolio [get]
func (h *Handler) streamPortfolio(c *gin.Context) {
	accountID, ok := h.resolveAccount(c)
	if !ok {
		return
	}
	h.sseStream(c, fmt.Sprintf(natsapi.SubjPortfolioSynced, accountID))
}

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
