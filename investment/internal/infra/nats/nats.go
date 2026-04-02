package nats

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/fx"

	"mallow/investment/internal/config"
)

var Module = fx.Module("nats",
	fx.Provide(NewConn),
	fx.Provide(NewJetStream),
)

// NewConn connects to NATS and registers lifecycle hooks.
func NewConn(lc fx.Lifecycle, cfg *config.Config, logger *slog.Logger) (*nats.Conn, error) {
	nc, err := nats.Connect(cfg.NATS.URL,
		nats.Name("investment-service"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
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
		return nil, fmt.Errorf("nats connect: %w", err)
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

// NewJetStream returns a JetStream context and ensures required streams exist.
func NewJetStream(nc *nats.Conn, logger *slog.Logger) (nats.JetStreamContext, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("jetstream context: %w", err)
	}

	// Stream for incoming transaction events from orchestrator bots
	if err := ensureStream(js, &nats.StreamConfig{
		Name:       "INVESTMENT_TRANSACTIONS",
		Subjects:   []string{"investment.transactions.>"},
		MaxAge:     7 * 24 * time.Hour,
		Duplicates: 7 * 24 * time.Hour, // dedup by Nats-Msg-Id (orchID+orderID)
		Storage:    nats.FileStorage,
		Retention:  nats.LimitsPolicy,
		Replicas:   1,
	}); err != nil {
		logger.Warn("ensure INVESTMENT_TRANSACTIONS stream", "error", err)
	}

	// Stream for outgoing audit / replay events from investment service
	if err := ensureStream(js, &nats.StreamConfig{
		Name:      "INVESTMENT_EVENTS",
		Subjects:  []string{"investment.events.>"},
		MaxAge:    30 * 24 * time.Hour,
		Storage:   nats.FileStorage,
		Retention: nats.LimitsPolicy,
		Replicas:  1,
	}); err != nil {
		logger.Warn("ensure INVESTMENT_EVENTS stream", "error", err)
	}

	logger.Info("JetStream streams ready")
	return js, nil
}

func ensureStream(js nats.JetStreamContext, cfg *nats.StreamConfig) error {
	_, err := js.StreamInfo(cfg.Name)
	if err == nats.ErrStreamNotFound {
		_, err = js.AddStream(cfg)
		return err
	}
	return err
}
