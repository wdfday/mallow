// Package ws implements the gateway's realtime WebSocket fan-out.
//
// One multiplexed endpoint (GET /api/v1/stream) carries two kinds of data over a
// single connection, distinguished by the WebSocket frame opcode:
//
//   - Market (binary frames): bars the client explicitly subscribes to. Relayed
//     verbatim from core NATS subject bars.{symbol}; best-effort (dropped when the
//     client is slow).
//   - Account (text/JSON frames): helm events, trade fills and portfolio syncs
//     scoped to the authenticated user. Read from JetStream so they can be replayed
//     by sequence on reconnect; never dropped (a slow client is disconnected and
//     resumes from its last sequence).
//
// Account scoping is enforced by matching the user_id embedded in each event
// payload (set by helm, trusted on the internal NATS network) against the user_id
// from the validated JWT.
//
// See docs/realtime-streaming.md for the full design and docs/realtime-streaming-plan.md.
package ws

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"

	"gateway/internal/service"
)

// idKind selects which set of owned ids a stream is keyed by.
type idKind int

const (
	keyedByHelm idKind = iota // subject = prefix + helm_id
	keyedByAccount
)

// accountChannel maps a client-facing channel name to its JetStream stream and
// the per-id subject prefix. One subject-filtered consumer is created per owned
// id, so a connection only ever receives its own user's events. subject is
// subjPrefix + id + subjSuffix (subjSuffix carries a token wildcard for streams
// whose subject has extra tokens after the owned id, e.g. helm.pos).
type accountChannel struct {
	ch         string // client-facing channel name (envelope "ch")
	stream     string // JetStream stream name
	subjPrefix string // subject = subjPrefix + id + subjSuffix
	subjSuffix string // "" for exact-keyed subjects, ".>" for deeper subjects
	keyedBy    idKind
}

// accountChannels are auto-subscribed (scoped to the user's resolved ids) on
// every connection. Channels keyed by helm_id/account_id are scoped purely by
// subject. Payloads that carry "user_id" (helm/trade/portfolio) are additionally
// re-checked as a defense-in-depth backstop; equity/position payloads omit it and
// rely on the scoped subject alone.
var accountChannels = []accountChannel{
	{ch: "event", stream: "HELM_EVENTS", subjPrefix: "helm.events.", keyedBy: keyedByHelm},
	{ch: "trade", stream: "HELM_TRADES", subjPrefix: "helm.trades.", subjSuffix: ".>", keyedBy: keyedByHelm},
	{ch: "fill", stream: "TRADE_FILLS", subjPrefix: "trade.filled.", keyedBy: keyedByAccount},
	{ch: "portfolio", stream: "PORTFOLIO_SYNC", subjPrefix: "portfolio.synced.", keyedBy: keyedByAccount},
	{ch: "equity", stream: "HELM_EQUITY", subjPrefix: "helm.equity.", keyedBy: keyedByHelm},
	{ch: "position", stream: "HELM_POSITIONS", subjPrefix: "helm.pos.", subjSuffix: ".>", keyedBy: keyedByHelm},
}

// Hub bridges NATS/JetStream to WebSocket clients.
type Hub struct {
	nc   *nats.Conn
	js   nats.JetStreamContext
	helm *service.HelmClient

	// AllowedOrigins is the CORS origin whitelist enforced on WS upgrade.
	// Empty = allow all (dev / non-browser clients).
	allowedOrigins map[string]struct{}

	upgrader websocket.Upgrader
}

// NewHub constructs a Hub. js may be nil; account channels are then disabled and
// only market subscriptions work (the connection logs and continues).
func NewHub(nc *nats.Conn, js nats.JetStreamContext, helm *service.HelmClient, allowedOrigins []string) *Hub {
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		origins[o] = struct{}{}
	}
	h := &Hub{nc: nc, js: js, helm: helm, allowedOrigins: origins}
	h.upgrader = websocket.Upgrader{
		// Echo the "bearer" subprotocol so browsers accept the upgrade. The JWT
		// is carried as the second subprotocol token (see auth middleware) — it is
		// NOT echoed back.
		Subprotocols: []string{"bearer"},
		CheckOrigin: func(r *http.Request) bool {
			if len(h.allowedOrigins) == 0 {
				return true
			}
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients omit Origin
			}
			_, ok := h.allowedOrigins[origin]
			return ok
		},
	}
	return h
}

// HandleStream upgrades the request and runs a per-connection actor. JWTAuth must
// run before this so the user id is present in the gin context.
func (h *Hub) HandleStream(c *gin.Context) {
	userID, _ := c.Get("user_id")
	uid, _ := userID.(string)
	if uid == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": "UNAUTHORIZED", "message": "missing user id"})
		return
	}

	wsConn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		slog.Error("ws upgrade failed", "err", err)
		return
	}

	conn := newConn(h, wsConn, uid)
	conn.run()
}
