package nats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/fx"

	"mallow/helm/internal/config"
)

// New connects to NATS and registers Drain on fx shutdown.
func New(cfg *config.Config, lc fx.Lifecycle) (*nats.Conn, error) {
	slog.Info("connecting to NATS", "url", cfg.Infra.NATSURL)
	nc, err := nats.Connect(cfg.Infra.NATSURL)
	if err != nil {
		return nil, err
	}
	slog.Info("connected to NATS")

	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			slog.Info("draining NATS connection")
			return nc.Drain()
		},
	})

	return nc, nil
}

// NewJetStream creates a JetStream context and ensures the USER_EVENTS stream exists.
func NewJetStream(nc *nats.Conn) (nats.JetStreamContext, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream context: %w", err)
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     "USER_EVENTS",
		Subjects: []string{"user.>"},
		Storage:  nats.FileStorage,
		MaxAge:   7 * 24 * time.Hour,
	})
	if err != nil {
		if _, infoErr := js.StreamInfo("USER_EVENTS"); infoErr != nil {
			return nil, fmt.Errorf("create stream USER_EVENTS: %w", err)
		}
	}

	return js, nil
}
