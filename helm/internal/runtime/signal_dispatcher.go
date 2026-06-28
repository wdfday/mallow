package runtime

import (
	"log/slog"
	"mallow/helm/internal/infra/herald"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/strategy"
)

// DispatcherMetrics holds NATS-level counters for the signal dispatcher.
// All fields use sync/atomic and are safe for concurrent access.
type DispatcherMetrics struct {
	// SignalsTotal is the total number of SignalResponse payloads received from NATS.
	SignalsTotal atomic.Int64
	// SignalsMissingID counts payloads with an empty helm_id or hand_id — typically
	// indicates a herald misconfiguration or a publish race during startup.
	SignalsMissingID atomic.Int64
	// SignalsNilPayload counts payloads where the inner Signal proto was nil.
	SignalsNilPayload atomic.Int64
	// SignalsDispatched counts signals successfully handed to RouteSignal.
	SignalsDispatched atomic.Int64
}

// SignalDispatcher bridges the NATS "signals" subject into helm hand channels.
// Both helm_id and hand_id are read from SignalResponse payload — subject is fixed "signals".
type SignalDispatcher struct {
	sink    SignalSink
	Metrics DispatcherMetrics
}

func NewSignalDispatcher(sink SignalSink) *SignalDispatcher {
	return &SignalDispatcher{sink: sink}
}

// Dispatch routes the signal in the response to the target helm/hand.
// receivedAt is the NATS server ingestion timestamp, used for TTL checks in each hand.
// Called directly from the NATS subscription goroutine — must not block.
func (d *SignalDispatcher) Dispatch(resp *herald.SignalResponse, receivedAt time.Time) {
	d.Metrics.SignalsTotal.Add(1)

	helmID := resp.HelmId
	handID := resp.HandId
	if helmID == "" || handID == "" {
		d.Metrics.SignalsMissingID.Add(1)
		slog.Warn("signal dispatcher: missing helm_id or hand_id in payload",
			"helm_id", helmID, "hand_id", handID)
		return
	}

	sig := resp.Signal
	if sig == nil {
		d.Metrics.SignalsNilPayload.Add(1)
		slog.Warn("signal dispatcher: nil signal payload", "helm_id", helmID, "hand_id", handID)
		return
	}
	// Strip the herald "exchange:" prefix (e.g. "binance:ETHUSDT" → "ETHUSDT")
	// so the symbol matches the hand's configured bare ticker.
	_, sym, ok := strings.Cut(sig.S, ":")
	if !ok {
		sym = sig.S
	}
	s := Signal{
		Symbol:      sym,
		Direction:   strategy.Direction(sig.Dir),
		Strength:    sig.Strength,
		ReceivedAt:  receivedAt,
		GeneratedAt: msToTime(sig.T),
	}
	if sig.Price != nil {
		s.Price = decimal.NewFromFloat(*sig.Price)
	}
	if sig.TargetPrice != nil {
		s.TargetPrice = decimal.NewFromFloat(*sig.TargetPrice)
	}
	if sig.StopPrice != nil {
		s.StopPrice = decimal.NewFromFloat(*sig.StopPrice)
	}
	if sig.IsOffset != nil {
		s.IsOffset = *sig.IsOffset
	}
	if sig.Atr != nil {
		s.ATR = decimal.NewFromFloat(*sig.Atr)
	}
	if sig.Reason != nil {
		s.Reason = *sig.Reason
	}
	slog.Debug("signal: dispatched to hand",
		"helm_id", helmID,
		"hand_id", handID,
		"symbol", s.Symbol,
		"dir", s.Direction,
		"strength", s.Strength,
		"urgent", s.IsUrgent(),
	)
	d.Metrics.SignalsDispatched.Add(1)
	d.sink.RouteSignal(helmID, handID, s)
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
