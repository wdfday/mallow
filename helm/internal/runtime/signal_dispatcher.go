package runtime

import (
	"log/slog"
	"time"

	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/engine"
)

// SignalDispatcher bridges the NATS "signals" subject into helm hand channels.
// Both helm_id and hand_id are read from SignalResponse payload — subject is fixed "signals".
type SignalDispatcher struct {
	sink SignalSink
}

func NewSignalDispatcher(sink SignalSink) *SignalDispatcher {
	return &SignalDispatcher{sink: sink}
}

// Dispatch routes the signal in the response to the target helm/hand.
// Called directly from the NATS subscription goroutine — must not block.
func (d *SignalDispatcher) Dispatch(resp *engine.SignalResponse) {
	helmID := resp.HelmId
	handID := resp.HandId
	if helmID == "" || handID == "" {
		slog.Warn("signal dispatcher: missing helm_id or hand_id in payload",
			"helm_id", helmID, "hand_id", handID)
		return
	}

	sig := resp.Signal
	if sig == nil {
		return
	}
	s := Signal{
		Symbol:     sig.S,
		Direction:  sig.Dir,
		Strength:   sig.Strength,
		ReceivedAt: time.Now(),
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
	d.sink.RouteSignal(helmID, handID, s)
}
