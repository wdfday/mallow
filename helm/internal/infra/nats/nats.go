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
			Name:     "SIGNALS",
			Subjects: []string{"signals"},
			Storage:  nats.MemoryStorage,
			MaxAge:   60 * time.Second,
			MaxMsgs:  10_000,
		},
		{
			Name:       "TRADE_FILLS",
			Subjects:   []string{"trade.filled.>"},
			Storage:    nats.FileStorage,
			Duplicates: 24 * time.Hour,
		},
		{
			Name:       "HELM_POSITIONS",
			Subjects:   []string{"helm.pos.>"},
			Storage:    nats.FileStorage,
			MaxAge:     30 * 24 * time.Hour,
			Duplicates: 2 * time.Minute,
		},
		{
			Name:       "HELM_TRADES",
			Subjects:   []string{"helm.trades.>"},
			Storage:    nats.FileStorage,
			MaxAge:     365 * 24 * time.Hour,
			Duplicates: 30 * time.Minute,
		},
		{
			Name:       "HELM_EQUITY",
			Subjects:   []string{"helm.equity.>"},
			Storage:    nats.FileStorage,
			MaxAge:     90 * 24 * time.Hour,
			Duplicates: 5 * time.Minute,
		},
		{
			Name:     "PORTFOLIO_SNAPSHOTS",
			Subjects: []string{"portfolio.>"},
			Storage:  nats.FileStorage,
			MaxAge:   90 * 24 * time.Hour,
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
