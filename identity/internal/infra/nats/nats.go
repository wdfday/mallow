package infraNats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"mallow/identity/internal/config"

	nats "github.com/nats-io/nats.go"
	"go.uber.org/fx"
)

// UserEventsStream is the JetStream stream that carries user lifecycle events.
const UserEventsStream = "USER_EVENTS"

// Module provides *nats.Conn and nats.JetStreamContext to fx.
var Module = fx.Module("nats", fx.Provide(New, NewJetStream))

// New connects to NATS and registers a graceful drain on app stop.
func New(lc fx.Lifecycle, cfg *config.Config, logger *slog.Logger) (*nats.Conn, error) {
	nc, err := nats.Connect(cfg.NATS.URL,
		nats.Name("identity-service"),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				logger.Warn("NATS disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			logger.Info("NATS reconnected")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect %s: %w", cfg.NATS.URL, err)
	}

	lc.Append(fx.Hook{
		OnStop: func(_ context.Context) error {
			nc.Drain()
			return nil
		},
	})

	logger.Info("Connected to NATS", "url", cfg.NATS.URL)
	return nc, nil
}

// NewJetStream creates a JetStream context and ensures the USER_EVENTS stream exists.
func NewJetStream(nc *nats.Conn, logger *slog.Logger) (nats.JetStreamContext, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream context: %w", err)
	}

	// Idempotent — create stream if it doesn't exist yet.
	_, err = js.AddStream(&nats.StreamConfig{
		Name:     UserEventsStream,
		Subjects: []string{"user.>"},
		Storage:  nats.FileStorage,
		MaxAge:   7 * 24 * time.Hour, // retain events for 7 days
	})
	if err != nil {
		// Stream may already exist — check if the error is benign.
		info, infoErr := js.StreamInfo(UserEventsStream)
		if infoErr != nil || info == nil {
			return nil, fmt.Errorf("create stream %s: %w", UserEventsStream, err)
		}
		// Stream already exists — that's fine.
	}

	logger.Info("JetStream ready", "stream", UserEventsStream)
	return js, nil
}
