package herald

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

// Subjects for herald registry protocol.
const (
	SubjEngRegister  = "engine.register"
	SubjEngValidate  = "engine.validate"
	SubjEngDeregist  = "engine.deregister"
	SubjEngList      = "engine.list"
	SubjEngPing      = "engine.ping"
	SubjEngHeartbeat = "engine.heartbeat"
	SubjEngReady     = "engine.ready"
)

// Type aliases — callers import only this package.
type (
	RegisterMsg       = market.RegisterMsg
	DeregisterMsg     = market.DeregisterMsg
	HandInfo          = market.HandInfo
	HandListResponse  = market.HandListResponse
	PingResponse      = market.PingResponse
	HeartbeatRequest  = market.HeartbeatRequest
	HeartbeatResponse = market.HeartbeatResponse
	ReadyEvent        = market.ReadyEvent
)

// Client handles registry-level communication with the Rust signal-engine over NATS.
// Serialization: protobuf for requests; JSON for acks.
type Client struct {
	nc *nats.Conn
}

func New(nc *nats.Conn) *Client {
	return &Client{nc: nc}
}

// Register registers a hand with the signal-engine registry.
// ctx must carry a deadline; use RegisterHand for the timeout-managed path.
func (c *Client) Register(ctx context.Context, req *RegisterMsg) error {
	payload, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("engine.register marshal: %w", err)
	}
	reply, err := c.nc.RequestWithContext(ctx, SubjEngRegister, payload)
	if err != nil {
		return fmt.Errorf("%w: engine.register: %w", ErrUnavailable, err)
	}
	var ack ackMsg
	if err := json.Unmarshal(reply.Data, &ack); err != nil {
		return fmt.Errorf("%w: engine.register: parse ack: %w", ErrUnavailable, err)
	}
	if !ack.OK {
		slog.Warn("engine.register rejected", "hand_id", req.HandId, "symbol", req.Symbol, "reason", ack.Error)
		return fmt.Errorf("%w: engine.register: %s", ErrRejected, ack.Error)
	}
	return nil
}

// Validate performs a dry-run check: validates timeframe, script syntax, and symbol availability.
// ctx must carry a deadline; callers are responsible for timeout.
func (c *Client) Validate(ctx context.Context, req *RegisterMsg) error {
	payload, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("engine.validate marshal: %w", err)
	}
	reply, err := c.nc.RequestWithContext(ctx, SubjEngValidate, payload)
	if err != nil {
		return fmt.Errorf("%w: engine.validate: %w", ErrUnavailable, err)
	}
	var ack ackMsg
	if err := json.Unmarshal(reply.Data, &ack); err != nil {
		return fmt.Errorf("%w: engine.validate: parse ack: %w", ErrUnavailable, err)
	}
	if !ack.OK {
		slog.Warn("engine.validate rejected", "symbol", req.Symbol, "timeframe", req.Timeframe, "reason", ack.Error)
		return fmt.Errorf("%w: engine.validate: %s", ErrRejected, ack.Error)
	}
	return nil
}

// Deregister removes a hand from the registry. Empty handID removes ALL hands.
// ctx must carry a deadline; use DeregisterHand for the timeout-managed path.
func (c *Client) Deregister(ctx context.Context, handID string) error {
	payload, err := proto.Marshal(&market.DeregisterMsg{HandId: handID})
	if err != nil {
		return fmt.Errorf("engine.deregister marshal: %w", err)
	}
	reply, err := c.nc.RequestWithContext(ctx, SubjEngDeregist, payload)
	if err != nil {
		return fmt.Errorf("%w: engine.deregister: %w", ErrUnavailable, err)
	}
	var ack ackMsg
	if err := json.Unmarshal(reply.Data, &ack); err != nil {
		return fmt.Errorf("%w: engine.deregister: parse ack: %w", ErrUnavailable, err)
	}
	if !ack.OK {
		slog.Warn("engine.deregister rejected", "hand_id", handID, "reason", ack.Error)
		return fmt.Errorf("%w: engine.deregister: %s", ErrRejected, ack.Error)
	}
	return nil
}

// ackMsg is the JSON envelope herald sends back for register/validate/deregister.
type ackMsg struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ListHands queries the signal-engine for all registered hands.
func (c *Client) ListHands(ctx context.Context) (*HandListResponse, error) {
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reply, err := c.nc.RequestWithContext(ctx2, SubjEngList, nil)
	if err != nil {
		return nil, fmt.Errorf("engine.list: %w", err)
	}
	var resp market.HandListResponse
	if err := proto.Unmarshal(reply.Data, &resp); err != nil {
		return nil, fmt.Errorf("engine.list: decode: %w", err)
	}
	return &resp, nil
}

// Ping returns herald's liveness response including the current herald_id.
func (c *Client) Ping(ctx context.Context) (*PingResponse, error) {
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

// Heartbeat sends the expected hand IDs for a helm. Herald returns missing and orphan sets.
func (c *Client) Heartbeat(ctx context.Context, helmID string, handIDs []string) (*HeartbeatResponse, error) {
	payload, err := proto.Marshal(&market.HeartbeatRequest{HelmId: helmID, Hands: handIDs})
	if err != nil {
		return nil, fmt.Errorf("engine.heartbeat marshal: %w", err)
	}
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	reply, err := c.nc.RequestWithContext(ctx2, SubjEngHeartbeat, payload)
	if err != nil {
		return nil, fmt.Errorf("engine.heartbeat: %w", err)
	}
	var resp market.HeartbeatResponse
	if err := proto.Unmarshal(reply.Data, &resp); err != nil {
		return nil, fmt.Errorf("engine.heartbeat: decode: %w", err)
	}
	return &resp, nil
}

// SubscribeReady subscribes to engine.ready published by herald on startup.
// cb receives the ReadyEvent. Returns a subscription for draining on shutdown.
func (c *Client) SubscribeReady(cb func(ev *ReadyEvent)) (*nats.Subscription, error) {
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

// ValidateHand performs a dry-run strategy validation for a single symbol.
// Implements runtime.HeraldRegistrar.
func (c *Client) ValidateHand(ctx context.Context, helmID, exchangeName, symbol, script, timeframe string, isFuture bool) error {
	req := &market.RegisterMsg{
		HandId:    "validate",
		Symbol:    symbol,
		Exchange:  exchangeName,
		IsFuture:  isFuture,
		Script:    script,
		HelmId:    helmID,
		Timeframe: timeframe,
	}
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return c.Validate(ctx2, req)
}

// RegisterHand registers a single (hand, symbol) pair.
// Implements runtime.HeraldRegistrar.
func (c *Client) RegisterHand(ctx context.Context, handID, helmID, exchangeName, symbol, script, timeframe string, isFuture bool) error {
	req := &market.RegisterMsg{
		HandId:    handID,
		Symbol:    symbol,
		Exchange:  exchangeName,
		IsFuture:  isFuture,
		Script:    script,
		HelmId:    helmID,
		Timeframe: timeframe,
	}
	ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.Register(ctx2, req)
}

// DeregisterHand removes a hand from the registry.
// Implements runtime.HeraldRegistrar.
func (c *Client) DeregisterHand(ctx context.Context, handID string) error {
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Deregister(ctx2, handID); err != nil {
		slog.Warn("engine.deregister failed", "hand_id", handID, "err", err)
		return err
	}
	return nil
}
