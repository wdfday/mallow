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

// NewJetStream creates a JetStream context and ensures required streams exist.
func NewJetStream(nc *nats.Conn) (nats.JetStreamContext, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream context: %w", err)
	}

	streams := []nats.StreamConfig{
		{
			Name:     "USER_EVENTS",
			Subjects: []string{"user.>"},
			Storage:  nats.FileStorage,
			MaxAge:   7 * 24 * time.Hour,
		},
		{
			Name:     "HELM_ACCOUNTS",
			Subjects: []string{"helm.accounts.*"},
			Storage:  nats.FileStorage,
			MaxAge:   7 * 24 * time.Hour,
		},
		{
			// Signals from herald — short-lived, memory only.
			// Allows helm to consume missed signals after a restart.
			// Messages older than MaxAge are dropped automatically.
			Name:     "SIGNALS",
			Subjects: []string{"signals"},
			Storage:  nats.MemoryStorage,
			MaxAge:   60 * time.Second,
			MaxMsgs:  10_000,
		},
	}
	for _, cfg := range streams {
		if err := ensureStream(js, cfg); err != nil {
			return nil, err
		}
	}

	return js, nil
}

func ensureStream(js nats.JetStreamContext, cfg nats.StreamConfig) error {
	_, err := js.AddStream(&cfg)
	if err == nil {
		return nil
	}
	if _, infoErr := js.StreamInfo(cfg.Name); infoErr == nil {
		return nil // stream already exists
	}
	return fmt.Errorf("create stream %s: %w", cfg.Name, err)
}
