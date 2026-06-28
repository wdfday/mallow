package engine

import (
	"context"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	market "mallow/helm/internal/proto"
)

// Subjects owned by SignalClient (signal subscription + bar publishing + engine reset).
// Registry subjects (register/validate/deregister/list/ping/heartbeat/ready) live in infra/herald.
const (
	SubjBarsPrefix = "bars."   // bars.{symbol}
	SubjSignals    = "signals" // all signals — orch_id and bot_id are in the payload
	SubjEngReset   = "engine.reset"
)

// signalConsumer is the durable JetStream consumer name for the SIGNALS stream.
const signalConsumer = "helm-signals"

// SignalClient handles signal subscription and bar publishing over NATS.
// Registry operations (register/deregister/validate) are in infra/herald.Client.
type SignalClient struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

func NewSignalClient(nc *nats.Conn, js nats.JetStreamContext) *SignalClient {
	return &SignalClient{nc: nc, js: js}
}

// PublishBar sends an OHLCV bar to `bars.{symbol}`.
func (c *SignalClient) PublishBar(ctx context.Context, bar *BarMsg) error {
	payload, err := proto.Marshal(bar)
	if err != nil {
		return err
	}
	return c.nc.Publish(SubjBarsPrefix+bar.S, payload)
}

// SubscribeSignals subscribes to the SIGNALS JetStream stream via a durable push consumer.
// On helm restart the consumer resumes from the last acked position.
// receivedAt is the NATS server ingestion timestamp; TTL enforcement is delegated to each Hand.
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
