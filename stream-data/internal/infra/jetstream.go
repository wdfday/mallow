package infra

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	"stream-data/internal/model"
	market "stream-data/proto/market"
)

// JetStreamPublisher publishes completed bars as BarMsg protobuf to bars.{symbol}
// using NATS JetStream for durable, at-least-once delivery.
//
// Stream "BARS" is auto-created on first connect with FileStorage and
// a 24-hour retention window — no manual setup required.
type JetStreamPublisher struct {
	js nats.JetStreamContext
}

// NewJetStreamPublisher enables JetStream on the given connection and ensures
// the "BARS" stream exists.
func NewJetStreamPublisher(nc *nats.Conn) (*JetStreamPublisher, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream context: %w", err)
	}

	if err := ensureBarsStream(js); err != nil {
		return nil, fmt.Errorf("ensure BARS stream: %w", err)
	}

	slog.Info("jetstream publisher ready")
	return &JetStreamPublisher{js: js}, nil
}

// RunBars consumes bars and publishes each as a BarMsg protobuf to bars.{symbol}.
// Returns when bars is closed.
func (p *JetStreamPublisher) RunBars(_ context.Context, bars <-chan model.Bar) {
	var published, errs int64

	for bar := range bars {
		msg := &market.BarMsg{
			T: bar.OpenTime,
			S: bar.Symbol,
			O: bar.Open,
			H: bar.High,
			L: bar.Low,
			C: bar.Close,
			V: bar.Volume,
		}

		data, err := proto.Marshal(msg)
		if err != nil {
			slog.Error("jetstream: proto marshal failed", "symbol", bar.Symbol, "err", err)
			errs++
			continue
		}

		subject := "bars." + bar.Symbol
		if _, err := p.js.Publish(subject, data); err != nil {
			slog.Error("jetstream: publish failed", "subject", subject, "err", err)
			errs++
			continue
		}

		published++
		slog.Debug("jetstream: bar published", "subject", subject, "open_time", bar.OpenTime)
	}

	slog.Info("jetstream publisher stopped", "published", published, "errors", errs)
}

// ensureBarsStream creates or updates the "BARS" JetStream stream.
func ensureBarsStream(js nats.JetStreamContext) error {
	want := &nats.StreamConfig{
		Name:     "BARS",
		Subjects: []string{"bars.>"},
		Storage:  nats.FileStorage,
		MaxAge:   24 * time.Hour,
	}

	info, err := js.StreamInfo("BARS")
	if err == nil {
		if info.Config.Storage != nats.FileStorage {
			slog.Warn("jetstream: BARS stream has wrong storage type, recreating", "old", info.Config.Storage)
			if err := js.DeleteStream("BARS"); err != nil {
				return fmt.Errorf("delete stream for recreation: %w", err)
			}
		} else {
			if _, err := js.UpdateStream(want); err != nil {
				return fmt.Errorf("update stream: %w", err)
			}
			slog.Info("jetstream: BARS stream ok", "storage", "file", "max_age", "24h")
			return nil
		}
	} else if !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("stream info: %w", err)
	}

	if _, err := js.AddStream(want); err != nil {
		return fmt.Errorf("add stream: %w", err)
	}

	slog.Info("jetstream: BARS stream created", "subjects", "bars.>", "storage", "file", "max_age", "24h")
	return nil
}
