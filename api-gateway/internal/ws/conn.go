package ws

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"

	"gateway/internal/metrics"
	"gateway/internal/service"
)

const (
	sendBuffer       = 256              // per-connection outbound queue
	writeWait        = 10 * time.Second // deadline for a single WS write
	pongWait         = 60 * time.Second // read deadline; reset on pong
	pingPeriod       = 20 * time.Second // must be < pongWait
	accountEnqueueTO = 5 * time.Second  // slow-client threshold for account frames
	maxControlMsg    = 4 * 1024         // max client control frame size
)

// outFrame is a pre-serialised frame queued for the single writer goroutine.
type outFrame struct {
	mt   int // websocket.TextMessage | websocket.BinaryMessage
	data []byte
}

// conn is a single WebSocket connection actor: one read loop (control), one write
// loop (drains send), plus NATS/JetStream subscriptions. All WS writes happen on
// the write loop goroutine only — gorilla is not concurrent-safe for writes.
type conn struct {
	hub    *Hub
	ws     *websocket.Conn
	userID string

	send chan outFrame

	closeOnce sync.Once
	done      chan struct{}

	scope service.UserScope // owned helm/account ids, resolved at connect

	mu          sync.Mutex
	barSubs     map[string][]*nats.Subscription // symbol → subscriptions (closed + forming)
	acctSubs    []*nats.Subscription
	acctStarted bool
}

func newConn(h *Hub, ws *websocket.Conn, userID string) *conn {
	return &conn{
		hub:     h,
		ws:      ws,
		userID:  userID,
		send:    make(chan outFrame, sendBuffer),
		done:    make(chan struct{}),
		barSubs: make(map[string][]*nats.Subscription),
	}
}

func (c *conn) run() {
	slog.Info("ws client connected", "user", c.userID, "remote", c.ws.RemoteAddr().String())
	metrics.WSConnOpened()
	defer metrics.WSConnClosed()
	go c.writeLoop()
	c.resolveScope()
	c.readLoop() // blocks until disconnect
	c.close()
	slog.Info("ws client disconnected", "user", c.userID)
}

// resolveScope asks helm which helms/accounts this user owns so account
// subscriptions can be filtered to its own subjects. On failure the connection
// still serves market data; the client can reconnect after linking an account.
func (c *conn) resolveScope() {
	if c.hub.helm == nil {
		return
	}
	scope, err := c.hub.helm.ResolveScope(c.userID)
	if err != nil {
		slog.Warn("ws scope resolve failed, account channels empty", "user", c.userID, "err", err)
		c.enqueueControl(wsError("scope_unavailable", "could not resolve account scope: "+err.Error()))
		return
	}
	c.scope = scope
}

// close tears down subscriptions and the socket exactly once.
func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.mu.Lock()
		for _, subs := range c.barSubs {
			for _, sub := range subs {
				_ = sub.Unsubscribe()
			}
		}
		for _, sub := range c.acctSubs {
			_ = sub.Unsubscribe()
		}
		c.mu.Unlock()
		_ = c.ws.Close()
	})
}

// ── read loop ────────────────────────────────────────────────────────────────

func (c *conn) readLoop() {
	c.ws.SetReadLimit(maxControlMsg)
	_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var msg clientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			c.enqueueControl(wsError("bad_message", "control message must be JSON"))
			continue
		}
		c.handleControl(msg)
	}
}

func (c *conn) handleControl(msg clientMsg) {
	switch msg.Op {
	case "hello":
		c.startAccountSubs(msg.Resume)
	case "ping":
		c.enqueueControl(ack("ping", ""))
	case "subscribe":
		// Account channels are auto-pushed and scoped — clients may not sub them.
		if isAccountCh(msg.Ch) {
			c.enqueueControl(wsError("forbidden", "channel "+msg.Ch+" is auto-scoped, do not subscribe"))
			return
		}
		c.startAccountSubs(nil) // implicit hello if the client skipped it
		c.subscribeBars(msg)
	case "unsubscribe":
		c.unsubscribeBars(msg)
	default:
		c.enqueueControl(wsError("unknown_op", "unknown op: "+msg.Op))
	}
}

func isAccountCh(ch string) bool {
	for _, a := range accountChannels {
		if a.ch == ch {
			return true
		}
	}
	return false
}

// ── market: bars (core NATS, best-effort) ────────────────────────────────────

func (c *conn) subscribeBars(msg clientMsg) {
	if msg.Ch != "bars" {
		c.enqueueControl(wsError("unsupported_channel", "market channel not supported: "+msg.Ch))
		return
	}
	symbol := strings.TrimSpace(msg.Symbol)
	if symbol == "" {
		c.enqueueControl(wsError("bad_request", "subscribe bars requires symbol"))
		return
	}
	tf := strings.TrimSpace(msg.TF)
	if tf == "" {
		tf = "M1"
	}
	// Subscriptions are keyed and framed by (tf, symbol) so the same symbol viewed
	// at different timeframes never collides. Must match the client's routing key.
	key := tf + ":" + symbol

	c.mu.Lock()
	if _, exists := c.barSubs[key]; exists {
		c.mu.Unlock()
		c.enqueueControl(ack("subscribe", key))
		return
	}
	c.mu.Unlock()

	relayWith := func(tag byte) func(*nats.Msg) {
		return func(m *nats.Msg) {
			frame := outFrame{mt: websocket.BinaryMessage, data: encodeMarketFrame(tag, key, m.Data)}
			c.enqueueMarket(frame)
		}
	}
	// Per-TF subjects: closed bars.{tf}.{symbol} → tagBars; forming
	// bars.live.{tf}.{symbol} → tagBarsLive. Closed feed the WASM chart state;
	// forming is the in-progress candle (client decides whether to apply it).
	subClosed, err := c.hub.nc.Subscribe("bars."+tf+"."+symbol, relayWith(tagBars))
	if err != nil {
		c.enqueueControl(wsError("subscribe_failed", err.Error()))
		return
	}
	subLive, err := c.hub.nc.Subscribe("bars.live."+tf+"."+symbol, relayWith(tagBarsLive))
	if err != nil {
		_ = subClosed.Unsubscribe()
		c.enqueueControl(wsError("subscribe_failed", err.Error()))
		return
	}

	c.mu.Lock()
	c.barSubs[key] = []*nats.Subscription{subClosed, subLive}
	c.mu.Unlock()
	c.enqueueControl(ack("subscribe", key))
}

func (c *conn) unsubscribeBars(msg clientMsg) {
	symbol := strings.TrimSpace(msg.Symbol)
	tf := strings.TrimSpace(msg.TF)
	if tf == "" {
		tf = "M1"
	}
	key := tf + ":" + symbol
	c.mu.Lock()
	subs := c.barSubs[key]
	delete(c.barSubs, key)
	c.mu.Unlock()
	for _, sub := range subs {
		_ = sub.Unsubscribe()
	}
	c.enqueueControl(ack("unsubscribe", key))
}

// ── account: scoped JetStream channels (replayable, never dropped) ────────────

// startAccountSubs creates one ephemeral JetStream push consumer per account
// channel, scoped to the connection's user. resume[ch] is the last stream
// sequence the client processed; delivery starts at the next sequence (or only
// new messages when absent). Idempotent — runs at most once per connection.
func (c *conn) startAccountSubs(resume map[string]uint64) {
	c.mu.Lock()
	if c.acctStarted {
		c.mu.Unlock()
		return
	}
	c.acctStarted = true
	c.mu.Unlock()

	if c.hub.js == nil {
		slog.Warn("ws account channels disabled: no JetStream context", "user", c.userID)
		return
	}

	for _, ac := range accountChannels {
		ids := c.scope.HelmIDs
		if ac.keyedBy == keyedByAccount {
			ids = c.scope.AccountIDs
		}
		// resume[ch] is the last stream sequence the client saw for this channel.
		// The seq is stream-global, so it applies to every subject-filtered
		// consumer in the channel's stream.
		seq, hasResume := resume[ac.ch]

		for _, id := range ids {
			opts := []nats.SubOpt{nats.BindStream(ac.stream), nats.AckNone()}
			if hasResume && seq > 0 {
				opts = append(opts, nats.StartSequence(seq+1))
			} else {
				opts = append(opts, nats.DeliverNew())
			}

			subject := ac.subjPrefix + id + ac.subjSuffix
			sub, err := c.hub.js.Subscribe(subject, func(m *nats.Msg) {
				c.handleAccountMsg(ac, m)
			}, opts...)
			if err != nil {
				// "stream not found" means the upstream service (helm) hasn't
				// started yet or NATS was restarted before helm recreated its
				// streams. Skip silently — the client will get events after
				// reconnect once helm is up. Any other error is worth logging.
				if strings.Contains(err.Error(), "stream not found") {
					slog.Warn("ws account subscribe: stream not found, skipping channel until helm is up",
						"ch", ac.ch, "subject", subject, "user", c.userID)
				} else {
					slog.Error("ws account subscribe failed", "subject", subject, "user", c.userID, "err", err)
					c.enqueueControl(wsError("account_subscribe_failed", ac.ch+": "+err.Error()))
				}
				continue
			}
			c.mu.Lock()
			c.acctSubs = append(c.acctSubs, sub)
			c.mu.Unlock()
		}
	}
}

func (c *conn) handleAccountMsg(ac accountChannel, m *nats.Msg) {
	// Defense-in-depth: subscriptions are already scoped to the user's own
	// subjects (subject keyed by a resolved helm_id/account_id), but when the
	// payload carries a user_id (helm.events, trade, portfolio) re-check it so a
	// resolve/subscribe mismatch can never leak another user's events. Streams
	// whose payload omits user_id (equity, pos) rely on the scoped subject alone.
	var hdr struct {
		UserID string `json:"user_id"`
	}
	_ = json.Unmarshal(m.Data, &hdr)
	if hdr.UserID != "" && hdr.UserID != c.userID {
		return
	}

	var seq uint64
	if meta, err := m.Metadata(); err == nil {
		seq = meta.Sequence.Stream
	}

	env := accountEnvelope{
		Ch:   ac.ch,
		Key:  lastToken(m.Subject),
		Seq:  seq,
		Data: json.RawMessage(m.Data),
	}
	data, err := json.Marshal(env)
	if err != nil {
		return
	}
	c.enqueueAccount(outFrame{mt: websocket.TextMessage, data: data})
}

func lastToken(subject string) string {
	if i := strings.LastIndexByte(subject, '.'); i >= 0 {
		return subject[i+1:]
	}
	return subject
}

// ── enqueue policies ──────────────────────────────────────────────────────────

// enqueueMarket is best-effort: if the client is slow and the queue is full, the
// bar is dropped (the next one supersedes it).
func (c *conn) enqueueMarket(f outFrame) {
	select {
	case c.send <- f:
	case <-c.done:
	default:
		// queue full — drop this market frame
	}
}

// enqueueControl behaves like account frames (must be delivered) but is small.
func (c *conn) enqueueControl(b []byte) {
	c.enqueueAccount(outFrame{mt: websocket.TextMessage, data: b})
}

// enqueueAccount must not drop. If the client cannot keep up within
// accountEnqueueTO, the connection is closed so the client reconnects and
// replays missed events from its last sequence.
func (c *conn) enqueueAccount(f outFrame) {
	select {
	case c.send <- f:
		return
	case <-c.done:
		return
	default:
	}
	timer := time.NewTimer(accountEnqueueTO)
	defer timer.Stop()
	select {
	case c.send <- f:
	case <-c.done:
	case <-timer.C:
		slog.Warn("ws slow client, closing for replay", "user", c.userID)
		c.close()
	}
}

// ── write loop (single writer) ────────────────────────────────────────────────

func (c *conn) writeLoop() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case f := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(f.mt, f.data); err != nil {
				c.close()
				return
			}
			if f.mt == websocket.BinaryMessage {
				metrics.WSFrame("market")
			} else {
				metrics.WSFrame("account")
			}
		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}
