package ws

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
)

// NATSBridge bridges NATS subjects to WebSocket clients.
type NATSBridge struct {
	NC             *nats.Conn
	AllowedOrigins map[string]struct{} // enforced on WS upgrade; nil/empty = allow all (dev)
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
	upgrader := websocket.Upgrader{
		// Enforce the CORS origin whitelist on WebSocket upgrades.
		// gorilla/websocket does not check Origin by default — without this,
		// any web page can open a WS connection to the gateway while the
		// user is logged in and receive live bar/signal data.
		CheckOrigin: func(r *http.Request) bool {
			if len(b.AllowedOrigins) == 0 {
				return true // dev / no-config fallback
			}
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients omit Origin
			}
			_, ok := b.AllowedOrigins[origin]
			return ok
		},
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "err", err)
		return
	}

	var (
		closeOnce sync.Once
		writeMu   sync.Mutex // gorilla/websocket is NOT concurrent-safe for writes
	)
	cleanup := func() {
		closeOnce.Do(func() { conn.Close() })
	}
	defer cleanup()

	// Subscribe to NATS subject, forward to WebSocket.
	// The NATS callback runs in a NATS-internal goroutine; conn.ReadMessage()
	// runs in this goroutine. Because gorilla/websocket is not concurrent-safe,
	// all writes are serialised through writeMu.
	sub, err := b.NC.Subscribe(subject, func(msg *nats.Msg) {
		writeMu.Lock()
		writeErr := conn.WriteMessage(websocket.TextMessage, msg.Data)
		writeMu.Unlock()
		if writeErr != nil {
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

	// Block until client disconnects (or cleanup() closes conn above).
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			slog.Info("ws client disconnected", "subject", subject, "remote", c.ClientIP())
			return
		}
	}
}
