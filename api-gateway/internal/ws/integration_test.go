//go:build integration

// Integration test for the gateway realtime WebSocket against a real NATS+JetStream.
//
//	docker run -d --name nats -p 14222:4222 nats -js
//	go test -tags integration ./internal/ws/ -run TestWS -v
//
// Verifies: subprotocol-bearer auth, bars binary relay, user-scoped account
// delivery (no cross-user leak), and reconnect resume by stream sequence.
package ws

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"
	"net/http/httptest"

	"gateway/internal/middleware"
	"gateway/internal/service"
)

const (
	itNatsURL   = "nats://localhost:14222"
	itSecret    = "test-secret"
	itUser      = "user-123"
	itOtherUser = "user-999"
	itHelm      = "helm-abc"
	itAccount   = "acct-xyz"
)

func mintToken(t *testing.T, sub string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(itSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func setupWS(t *testing.T) (*nats.Conn, nats.JetStreamContext, *httptest.Server) {
	t.Helper()
	nc, err := nats.Connect(itNatsURL)
	if err != nil {
		t.Fatalf("nats connect (is the docker NATS up on 14222?): %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct{ name, subj string }{
		{"HELM_EVENTS", "helm.events.>"},
		{"TRADE_FILLS", "trade.filled.>"},
		{"PORTFOLIO_SYNC", "portfolio.synced.>"},
		{"HELM_EQUITY", "helm.equity.>"},
		{"HELM_POSITIONS", "helm.pos.>"},
	} {
		_, _ = js.AddStream(&nats.StreamConfig{Name: s.name, Subjects: []string{s.subj}})
	}
	// Mimic helm.helms.list: this user owns one helm + account.
	if _, err := nc.Subscribe("helm.helms.list", func(m *nats.Msg) {
		data, _ := json.Marshal([]map[string]string{{"id": itHelm, "account_id": itAccount}})
		reply, _ := json.Marshal(map[string]any{"ok": true, "data": json.RawMessage(data)})
		_ = m.Respond(reply)
	}); err != nil {
		t.Fatal(err)
	}

	hub := NewHub(nc, js, service.NewHelmClient(nc), nil)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	jwtAuth := middleware.JWTAuth(middleware.JWTAuthConfig{Secret: itSecret}, nil, nil)
	r.GET("/api/v1/stream", middleware.WSBearerFromProtocol(), jwtAuth, hub.HandleStream)
	srv := httptest.NewServer(r)

	t.Cleanup(func() { srv.Close(); nc.Close() })
	return nc, js, srv
}

func dialWS(t *testing.T, srv *httptest.Server, token string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/stream"
	d := websocket.Dialer{Subprotocols: []string{"bearer", token}}
	c, resp, err := d.Dial(u, nil)
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial: %v (status=%d)", err, code)
	}
	return c
}

func TestWS_BarsRelay(t *testing.T) {
	nc, _, srv := setupWS(t)
	c := dialWS(t, srv, mintToken(t, itUser))
	defer c.Close()

	_ = c.WriteJSON(map[string]any{"op": "hello"})
	_ = c.WriteJSON(map[string]any{"op": "subscribe", "ch": "bars", "symbol": "BTCUSDT", "tf": "M1"})
	time.Sleep(300 * time.Millisecond) // let the bars subscription register

	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	if err := nc.Publish("bars.M1.BTCUSDT", payload); err != nil {
		t.Fatal(err)
	}

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if mt != websocket.BinaryMessage {
			continue // skip ack/control text frames
		}
		if data[0] != tagBars {
			t.Fatalf("tag = %x, want %x", data[0], tagBars)
		}
		klen := int(data[1])<<8 | int(data[2])
		if key := string(data[3 : 3+klen]); key != "M1:BTCUSDT" {
			t.Fatalf("key = %q", key)
		}
		if got := data[3+klen:]; !bytes.Equal(got, payload) {
			t.Fatalf("payload = %x, want %x", got, payload)
		}
		return // success
	}
}

func TestWS_AccountScope(t *testing.T) {
	nc, js, srv := setupWS(t)
	_ = js
	c := dialWS(t, srv, mintToken(t, itUser))
	defer c.Close()

	_ = c.WriteJSON(map[string]any{"op": "hello"}) // DeliverNew
	time.Sleep(400 * time.Millisecond)             // let account JetStream subs attach

	mkEvent := func(uid string) []byte {
		b, _ := json.Marshal(map[string]any{"user_id": uid, "helm_id": itHelm, "msg": "hi-" + uid})
		return b
	}
	// Publish for another user first, then for ours. Only ours must be delivered.
	_ = nc.Publish("helm.events."+itHelm, mkEvent(itOtherUser))
	_ = nc.Publish("helm.events."+itHelm, mkEvent(itUser))

	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		mt, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("read (expected our scoped event): %v", err)
		}
		if mt != websocket.TextMessage {
			continue
		}
		var env map[string]any
		if json.Unmarshal(data, &env) != nil {
			continue
		}
		if env["ch"] == "ack" || env["ch"] == "error" {
			continue
		}
		if env["ch"] == "helm" {
			d, _ := env["data"].(map[string]any)
			if d["user_id"] != itUser {
				t.Fatalf("scope leak: delivered event for user_id=%v", d["user_id"])
			}
			return // got our event; the other-user event was filtered server-side
		}
	}
}

func TestWS_Resume(t *testing.T) {
	nc, _, srv := setupWS(t)
	token := mintToken(t, itUser)

	mkEvent := func(tag string) []byte {
		b, _ := json.Marshal(map[string]any{"user_id": itUser, "helm_id": itHelm, "msg": tag})
		return b
	}
	readHelm := func(c *websocket.Conn) (uint64, string) {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("read helm event: %v", err)
			}
			if mt != websocket.TextMessage {
				continue
			}
			var env struct {
				Ch   string          `json:"ch"`
				Seq  uint64          `json:"seq"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(data, &env) != nil || env.Ch != "helm" {
				continue
			}
			var d struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(env.Data, &d)
			return env.Seq, d.Msg
		}
	}

	// Connection 1: receive E1, capture its seq, then disconnect.
	c1 := dialWS(t, srv, token)
	_ = c1.WriteJSON(map[string]any{"op": "hello"})
	time.Sleep(400 * time.Millisecond)
	_ = nc.Publish("helm.events."+itHelm, mkEvent("E1"))
	seq1, msg1 := readHelm(c1)
	if msg1 != "E1" {
		t.Fatalf("expected E1, got %q", msg1)
	}
	c1.Close()

	// Publish E2 while disconnected.
	_ = nc.Publish("helm.events."+itHelm, mkEvent("E2"))
	time.Sleep(200 * time.Millisecond)

	// Connection 2: resume from seq1 → must receive E2 (the missed event).
	c2 := dialWS(t, srv, token)
	defer c2.Close()
	_ = c2.WriteJSON(map[string]any{"op": "hello", "resume": map[string]uint64{"helm": seq1}})
	seq2, msg2 := readHelm(c2)
	if msg2 != "E2" {
		t.Fatalf("resume: expected E2, got %q (seq1=%d seq2=%d)", msg2, seq1, seq2)
	}
	if seq2 <= seq1 {
		t.Fatalf("resume: expected seq2 > seq1, got seq1=%d seq2=%d", seq1, seq2)
	}
}
