package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	market "mallow/helm/internal/proto"
)

// Subjects
const (
	SubjBarsPrefix   = "bars."   // bars.{symbol}
	SubjSignals      = "signals" // all signals — orch_id and bot_id are in the payload
	SubjEngReset     = "engine.reset"
	SubjEngRegister  = "engine.register"
	SubjEngValidate  = "engine.validate" // dry-run validate — no registry side effects
	SubjEngDeregist  = "engine.deregister"
	SubjEngList      = "engine.list"
	SubjEngPing      = "engine.ping"      // health check + herald_id
	SubjEngHeartbeat = "engine.heartbeat" // diff check: helm sends expected hands, herald returns missing
	SubjEngReady     = "engine.ready"     // herald publishes on startup
)

// signalConsumer is the durable JetStream consumer name for the SIGNALS stream.
// Durable consumers survive helm restarts and resume from the last acked message.
const signalConsumer = "helm-signals"

// SignalClient handles all communication with the Rust signal-engine over NATS.
// Serialization: protobuf for most subjects; JSON for register/deregister acks.
type SignalClient struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

func NewSignalClient(nc *nats.Conn, js nats.JetStreamContext) *SignalClient {
	return &SignalClient{nc: nc, js: js}
}

// ── Bar publishing ────────────────────────────────────────────────────────────

// PublishBar sends an OHLCV bar to `bars.{symbol}`.
func (c *SignalClient) PublishBar(ctx context.Context, bar *BarMsg) error {
	payload, err := proto.Marshal(bar)
	if err != nil {
		return err
	}
	return c.nc.Publish(SubjBarsPrefix+bar.S, payload)
}

// ── Signal subscription ───────────────────────────────────────────────────────

// SubscribeSignals subscribes to the SIGNALS JetStream stream via a durable
// push consumer. On helm restart the consumer resumes from the last acked
// position, delivering any signals buffered while helm was down.
//
// receivedAt passed to cb is the NATS server ingestion timestamp — more
// accurate than time.Now() and correctly reflects restart-replay lag.
// TTL enforcement is delegated to each Hand so it can use its own config.
func (c *SignalClient) SubscribeSignals(cb func(resp *SignalResponse, receivedAt time.Time)) (*nats.Subscription, error) {
	sub, err := c.js.Subscribe(SubjSignals, func(msg *nats.Msg) {
		var resp market.SignalResponse
		if err := proto.Unmarshal(msg.Data, &resp); err != nil {
			slog.Error("signal: failed to decode SignalResponse", "err", err)
			_ = msg.Ack()
			return
		}

		receivedAt := time.Now()
		if meta, err := msg.Metadata(); err == nil {
			receivedAt = meta.Timestamp
		}

		if resp.Signal != nil {
			slog.Debug("signal: nats received",
				"helm_id", resp.HelmId,
				"hand_id", resp.HandId,
				"symbol", resp.Signal.S,
				"dir", resp.Signal.Dir,
				"strength", resp.Signal.Strength,
				"js_age", time.Since(receivedAt).Truncate(time.Millisecond),
			)
		}

		_ = msg.Ack()
		cb(&resp, receivedAt)
	},
		nats.Durable(signalConsumer),
		nats.DeliverAll(),
		nats.AckExplicit(),
	)
	if err != nil {
		return nil, err
	}
	slog.Info("subscribed to signals (JetStream)", "subject", SubjSignals, "consumer", signalConsumer)
	return sub, nil
}

// ── Engine control ────────────────────────────────────────────────────────────

// Reset resets symbol state (engine.reset). Empty symbol = reset all.
func (c *SignalClient) Reset(ctx context.Context, symbol string) error {
	payload, err := proto.Marshal(&market.ResetMsg{Symbol: symbol})
	if err != nil {
		return err
	}
	target := "all"
	if symbol != "" {
		target = symbol
	}
	slog.Info("sending reset", "target", target)
	return c.nc.Publish(SubjEngReset, payload)
}

// ── Hand registry ─────────────────────────────────────────────────────────────

// Register registers a hand with the signal-engine registry.
// Ack is JSON {"ok":true} or {"ok":false,"error":"..."}.
func (c *SignalClient) Register(ctx context.Context, req *RegisterMsg) (string, error) {
	payload, err := proto.Marshal(req)
	if err != nil {
		return "", err
	}
	reply, err := c.nc.RequestWithContext(ctx, SubjEngRegister, payload)
	if err != nil {
		return "", err
	}
	var ack struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(reply.Data, &ack); err != nil {
		// Fallback: accept legacy plain-text "ok" during transition
		if string(reply.Data) == "ok" {
			slog.Info("engine.register ack (legacy)", "hand_id", req.HandId)
			return "ok", nil
		}
		return "", fmt.Errorf("parse register ack: %w", err)
	}
	if !ack.OK {
		slog.Warn("engine.register rejected", "hand_id", req.HandId, "error", ack.Error)
		return "", fmt.Errorf("herald register: %s", ack.Error)
	}
	slog.Info("engine.register ack", "hand_id", req.HandId)
	return "ok", nil
}

// Validate performs a dry-run check against the signal engine: validates
// timeframe, script syntax, and strategy compilation without registering the
// hand. Returns an error if the strategy is invalid.
func (c *SignalClient) Validate(ctx context.Context, req *RegisterMsg) error {
	payload, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reply, err := c.nc.RequestWithContext(ctx2, SubjEngValidate, payload)
	if err != nil {
		return fmt.Errorf("herald validate: %w", err)
	}
	var ack struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(reply.Data, &ack); err != nil {
		return fmt.Errorf("parse validate ack: %w", err)
	}
	if !ack.OK {
		return fmt.Errorf("invalid strategy: %s", ack.Error)
	}
	return nil
}

// Deregister removes a hand from the signal-engine registry.
// Empty handID removes ALL hands.
func (c *SignalClient) Deregister(ctx context.Context, handID string) error {
	payload, err := proto.Marshal(&market.DeregisterMsg{HandId: handID})
	if err != nil {
		return err
	}
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err = c.nc.RequestWithContext(ctx2, SubjEngDeregist, payload)
	if err != nil {
		return err
	}
	slog.Info("engine.deregister ack", "hand_id", handID)
	return nil
}

// ListHands queries the signal-engine for all registered hands (request/reply).
func (c *SignalClient) ListHands(ctx context.Context) (*HandListResponse, error) {
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reply, err := c.nc.RequestWithContext(ctx2, SubjEngList, nil)
	if err != nil {
		return nil, err
	}
	var resp market.HandListResponse
	if err := proto.Unmarshal(reply.Data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ── Health & handshake ────────────────────────────────────────────────────────

// Ping sends engine.ping and returns herald's PingResponse.
// Used to check liveness and detect herald restarts via herald_id change.
func (c *SignalClient) Ping(ctx context.Context) (*market.PingResponse, error) {
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reply, err := c.nc.RequestWithContext(ctx2, SubjEngPing, nil)
	if err != nil {
		return nil, err
	}
	var resp market.PingResponse
	if err := proto.Unmarshal(reply.Data, &resp); err != nil {
		return nil, fmt.Errorf("decode ping response: %w", err)
	}
	return &resp, nil
}

// Heartbeat sends the list of hand IDs helm expects to be registered under helmID.
// Herald returns which are missing and which are confirmed.
// Caller should re-register any missing hands.
func (c *SignalClient) Heartbeat(ctx context.Context, helmID string, handIDs []string) (*market.HeartbeatResponse, error) {
	req := &market.HeartbeatRequest{
		HelmId: helmID,
		Hands:  handIDs,
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reply, err := c.nc.RequestWithContext(ctx2, SubjEngHeartbeat, payload)
	if err != nil {
		return nil, err
	}
	var resp market.HeartbeatResponse
	if err := proto.Unmarshal(reply.Data, &resp); err != nil {
		return nil, fmt.Errorf("decode heartbeat response: %w", err)
	}
	return &resp, nil
}

// SubscribeReady subscribes to engine.ready — called by herald on startup.
// cb receives the ReadyEvent (herald_id, tf, symbols). Non-blocking; returns sub for drain.
func (c *SignalClient) SubscribeReady(cb func(ev *market.ReadyEvent)) (*nats.Subscription, error) {
	sub, err := c.nc.Subscribe(SubjEngReady, func(msg *nats.Msg) {
		var ev market.ReadyEvent
		if err := proto.Unmarshal(msg.Data, &ev); err != nil {
			slog.Error("failed to decode ReadyEvent", "err", err)
			return
		}
		slog.Info("engine.ready received", "herald_id", ev.HeraldId, "tf", ev.Tf, "symbols", len(ev.Symbols))
		cb(&ev)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("subscribed to engine.ready", "subject", SubjEngReady)
	return sub, nil
}
