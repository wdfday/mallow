package tick

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/nats-io/nats.go"
)

// Router subscribes to tick subjects (from stream-data) and forwards to BarBuilder.
// Ticks arrive on NATS subjects: ticks.{class}.{symbol}
//
// The Router decodes each tick and feeds it to the BarBuilder, which aggregates
// ticks into OHLCV bars and emits them via its callback.
type Router struct {
	nc      *nats.Conn
	builder *BarBuilder
}

// NewRouter creates a Router that feeds ticks into the given BarBuilder.
func NewRouter(nc *nats.Conn, builder *BarBuilder) *Router {
	return &Router{nc: nc, builder: builder}
}

// Run subscribes to tick subjects and processes messages until ctx is cancelled.
func (r *Router) Run(ctx context.Context) error {
	subject := "ticks.>" // ticks.{class}.{symbol}
	sub, err := r.nc.SubscribeSync(subject)
	if err != nil {
		return err
	}

	slog.Info("tick router subscribed", "subject", subject)

	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			if err == context.Canceled || err == context.DeadlineExceeded {
				// Flush remaining bars on shutdown.
				r.builder.FlushAll()
				return nil
			}
			return err
		}

		r.handleTick(msg)
	}
}

// natsTickMsg matches the msgpack/json format from stream-data.
type natsTickMsg struct {
	T   int64   `json:"t"`             // Unix ms
	S   string  `json:"s"`             // symbol
	Cls string  `json:"class"`         // stocks | crypto | forex
	Src string  `json:"src"`           // binance | alpaca | twelvedata
	P   float64 `json:"p"`             // price
	V   float64 `json:"v,omitempty"`   // volume
	Bid float64 `json:"bid,omitempty"` // bid
	Ask float64 `json:"ask,omitempty"` // ask
}

func (r *Router) handleTick(msg *nats.Msg) {
	var tick natsTickMsg

	// Try JSON first (stream-data currently uses JSON for NATS ticks).
	if err := json.Unmarshal(msg.Data, &tick); err != nil {
		slog.Debug("tick decode failed", "subject", msg.Subject, "err", err, "len", len(msg.Data))
		return
	}

	if tick.S == "" || tick.P <= 0 {
		slog.Debug("tick skipped: invalid data", "subject", msg.Subject)
		return
	}

	slog.Debug("tick received",
		"symbol", tick.S,
		"price", tick.P,
		"volume", tick.V,
		"source", tick.Src,
	)

	r.builder.AddTick(Tick{
		Timestamp: tick.T,
		Symbol:    tick.S,
		Price:     tick.P,
		Volume:    tick.V,
	})
}
