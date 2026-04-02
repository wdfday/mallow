package ws

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // allow all origins (dev)
}

// NATSBridge bridges NATS subjects to WebSocket clients.
type NATSBridge struct {
	NC *nats.Conn
}

// HandleTicks streams bars.> from NATS to WebSocket client.
func (b *NATSBridge) HandleTicks(c *gin.Context) {
	b.bridgeSubject(c, "bars.>")
}

// HandleSignals streams signals.> from NATS to WebSocket client.
func (b *NATSBridge) HandleSignals(c *gin.Context) {
	b.bridgeSubject(c, "signals.>")
}

func (b *NATSBridge) bridgeSubject(c *gin.Context, subject string) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "err", err)
		return
	}

	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() { conn.Close() })
	}
	defer cleanup()

	// Subscribe to NATS subject, forward to WebSocket
	sub, err := b.NC.Subscribe(subject, func(msg *nats.Msg) {
		if writeErr := conn.WriteMessage(websocket.TextMessage, msg.Data); writeErr != nil {
			slog.Debug("ws write failed, closing", "err", writeErr)
			cleanup()
		}
	})
	if err != nil {
		slog.Error("nats subscribe failed", "subject", subject, "err", err)
		return
	}
	defer sub.Unsubscribe()

	slog.Info("ws client connected", "subject", subject, "remote", c.ClientIP())

	// Block until client disconnects
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			slog.Info("ws client disconnected", "subject", subject, "remote", c.ClientIP())
			return
		}
	}
}
