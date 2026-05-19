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
)

// events godoc
// @Summary Stream real-time helm behavioral events via SSE
// @Tags helms
// @Security BearerAuth
// @Produce text/event-stream
// @Param id path string true "Helm ID"
// @Success 200 {string} string "SSE stream"
// @Failure 400 {object} shared.ErrorResponse
// @Failure 503 {object} shared.ErrorResponse
// @Router /api/v1/helms/{id}/events [get]
func (h *Handler) events(c *gin.Context) {
	helmID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		shared.RespondWithError(c, http.StatusBadRequest, "invalid id")
		return
	}

	if h.nc == nil {
		shared.RespondWithError(c, http.StatusServiceUnavailable, "event stream not available")
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	ch := make(chan []byte, 32)

	sub, err := h.nc.Subscribe(fmt.Sprintf(natsapi.SubjHelmEvents, helmID), func(msg *nats.Msg) {
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
