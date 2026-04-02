package infra

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// NewNATSConn creates a single shared NATS connection with reconnect handling.
// Caller is responsible for closing: nc.Drain() then nc.Close().
func NewNATSConn(url string) (*nats.Conn, error) {
	nc, err := nats.Connect(url,
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("nats: disconnected", "err", err)
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			slog.Info("nats: reconnected", "url", nc.ConnectedUrl())
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect %q: %w", url, err)
	}
	slog.Info("nats connected", "url", url)
	return nc, nil
}
