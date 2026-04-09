package engine

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	market "orchestrator/internal/proto"
)

// Subjects
const (
	SubjBarsPrefix    = "bars."     // bars.{symbol}
	SubjSignals       = "signals" // all signals — orch_id and bot_id are in the payload
	SubjEngConfigure  = "engine.configure"
	SubjEngReset      = "engine.reset"
	SubjEngRegister   = "engine.register"
	SubjEngDeregister = "engine.deregister"
	SubjEngList       = "engine.list"
)

// SignalClient handles all communication with the Rust signal-engine over NATS.
// Serialization: protobuf (matches signal-engine's prost encoding).
type SignalClient struct {
	nc *nats.Conn
}

func NewSignalClient(nc *nats.Conn) *SignalClient {
	return &SignalClient{nc: nc}
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

// SubscribeSignals subscribes to the "signals" subject.
// Both orch_id and bot_id are read from SignalResponse payload.
func (c *SignalClient) SubscribeSignals(cb func(resp *SignalResponse)) (*nats.Subscription, error) {
	sub, err := c.nc.Subscribe(SubjSignals, func(msg *nats.Msg) {
		var resp market.SignalResponse
		if err := proto.Unmarshal(msg.Data, &resp); err != nil {
			slog.Error("failed to decode SignalResponse", "err", err)
			return
		}
		cb(&resp)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("subscribed to signals", "subject", SubjSignals)
	return sub, nil
}

// ── Engine control (legacy) ───────────────────────────────────────────────────

// Configure sends a global strategy config (legacy engine.configure).
func (c *SignalClient) Configure(ctx context.Context, cfg *ConfigMsg) error {
	payload, err := proto.Marshal(cfg)
	if err != nil {
		return err
	}
	slog.Info("sending strategy config", "strategy", cfg.Strategy)
	return c.nc.Publish(SubjEngConfigure, payload)
}

// Reset resets symbol state (legacy engine.reset). Empty symbol = reset all.
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

// ── Bot registry ──────────────────────────────────────────────────────────────

// Register registers a bot with the signal-engine registry.
// Returns ack payload ("ok" or "error: ..."). Times out after 3s.
func (c *SignalClient) Register(ctx context.Context, req *RegisterMsg) (string, error) {
	payload, err := proto.Marshal(req)
	if err != nil {
		return "", err
	}
	reply, err := c.nc.RequestWithContext(ctx, SubjEngRegister, payload)
	if err != nil {
		return "", err
	}
	ack := strings.TrimSpace(string(reply.Data))
	slog.Info("engine.register ack", "bot_id", req.BotId, "ack", ack)
	return ack, nil
}

// Deregister removes a bot from the signal-engine registry.
// Empty bot_id removes ALL bots.
func (c *SignalClient) Deregister(ctx context.Context, botID string) error {
	payload, err := proto.Marshal(&market.DeregisterMsg{BotId: botID})
	if err != nil {
		return err
	}
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reply, err := c.nc.RequestWithContext(ctx2, SubjEngDeregister, payload)
	if err != nil {
		return err
	}
	slog.Info("engine.deregister ack", "bot_id", botID, "ack", string(reply.Data))
	return nil
}

// ListBots queries the signal-engine for all registered bots (request/reply).
func (c *SignalClient) ListBots(ctx context.Context) (*BotListResponse, error) {
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reply, err := c.nc.RequestWithContext(ctx2, SubjEngList, nil)
	if err != nil {
		return nil, err
	}
	var resp market.BotListResponse
	if err := proto.Unmarshal(reply.Data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
