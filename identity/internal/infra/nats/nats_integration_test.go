//go:build integration

package infraNats

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	natsio "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"mallow/identity/internal/config"
)

func TestNew_ConnectsToNATS(t *testing.T) {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	lc := fxtest.NewLifecycle(t)

	nc, err := New(lc, cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, nc)
	assert.True(t, nc.IsConnected())

	lc.RequireStart()
	lc.RequireStop()
}

func TestNew_BadNATSURL_ReturnsError(t *testing.T) {
	cfg := config.Load()
	cfg.NATS.URL = "nats://localhost:9999"
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	lc := fxtest.NewLifecycle(t)

	nc, err := New(lc, cfg, logger)
	assert.Error(t, err)
	assert.Nil(t, nc)
}

func TestNewJetStream_CreatesStream(t *testing.T) {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	nc, err := natsio.Connect(cfg.NATS.URL)
	require.NoError(t, err)
	defer nc.Close()

	js, err := NewJetStream(nc, logger)
	require.NoError(t, err)
	require.NotNil(t, js)

	info, err := js.StreamInfo(UserEventsStream)
	require.NoError(t, err)
	assert.Equal(t, UserEventsStream, info.Config.Name)
}

func TestNATS_PublishSubscribeRoundTrip(t *testing.T) {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	nc, err := natsio.Connect(cfg.NATS.URL)
	require.NoError(t, err)
	defer nc.Close()

	_, err = NewJetStream(nc, logger)
	require.NoError(t, err)

	received := make(chan []byte, 1)
	subject := "user.integration.test"

	sub, err := nc.Subscribe(subject, func(msg *natsio.Msg) {
		received <- msg.Data
	})
	require.NoError(t, err)
	defer sub.Unsubscribe()

	payload := []byte(`{"event":"test"}`)
	err = nc.Publish(subject, payload)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	select {
	case data := <-received:
		assert.Equal(t, payload, data)
	case <-ctx.Done():
		t.Fatal("timeout waiting for message")
	}
}
