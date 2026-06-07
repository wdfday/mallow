package poslog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	streamName  = "HELM_POSITIONS"
	subjectBase = "helm.pos"
)

// NatsLog is the JetStream-backed implementation of Log.
//
// Stream layout:
//
//	Name:     HELM_POSITIONS
//	Subjects: helm.pos.>
//	Subject per leg: helm.pos.{helm_id}.{hand_id}.{position_id}
//
// Dedup: NATS dedup window (2 min) keyed by Nats-Msg-Id = Event.ID.
// Replay uses ephemeral ordered consumers — no acks, no durable state.
type NatsLog struct {
	js nats.JetStreamContext
}

// NewNatsLog creates a NatsLog, ensuring the HELM_POSITIONS stream exists.
func NewNatsLog(js nats.JetStreamContext) (*NatsLog, error) {
	if _, err := js.StreamInfo(streamName); err != nil {
		return nil, fmt.Errorf("stream %s not found: %w", streamName, err)
	}
	return &NatsLog{js: js}, nil
}

// Publish appends one event to the stream. Idempotent within the dedup window.
func (l *NatsLog) Publish(ctx context.Context, e Event) error {
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("poslog marshal: %w", err)
	}
	subject := fmt.Sprintf("%s.%s.%s.%s", subjectBase, e.HelmID, e.HandID, e.PositionID)
	msg := nats.NewMsg(subject)
	msg.Data = data
	msg.Header.Set(nats.MsgIdHdr, e.ID)
	_, err = l.js.PublishMsg(msg, nats.Context(ctx))
	return err
}

// ReplayHand returns all events for a hand across all its position legs, in order.
func (l *NatsLog) ReplayHand(ctx context.Context, helmID, handID string) ([]Event, error) {
	filter := fmt.Sprintf("%s.%s.%s.>", subjectBase, helmID, handID)
	return l.drain(ctx, filter)
}

// ReplayLeg returns all events for a single position leg, in order.
func (l *NatsLog) ReplayLeg(ctx context.Context, helmID, handID, positionID string) ([]Event, error) {
	filter := fmt.Sprintf("%s.%s.%s.%s", subjectBase, helmID, handID, positionID)
	return l.drain(ctx, filter)
}

// TradesPaged returns at most limit completed trades for a hand.
// cursor=0 starts from the beginning of the stream; pass TradeRecord.Cursor+1 for the next page.
// Only position_closed events contribute to the result — other kinds are skipped.
func (l *NatsLog) TradesPaged(ctx context.Context, helmID, handID string, cursor uint64, limit int) (TradesPage, error) {
	if limit <= 0 {
		limit = 100
	}
	filter := fmt.Sprintf("%s.%s.%s.>", subjectBase, helmID, handID)

	var startOpt nats.SubOpt
	if cursor > 0 {
		startOpt = nats.StartSequence(cursor)
	} else {
		startOpt = nats.DeliverAll()
	}

	sub, err := l.js.SubscribeSync(filter,
		nats.OrderedConsumer(),
		startOpt,
		nats.Context(ctx),
	)
	if err != nil {
		return TradesPage{}, fmt.Errorf("poslog trades subscribe: %w", err)
	}
	defer sub.Unsubscribe()

	var trades []TradeRecord
	var lastSeq uint64

	for len(trades) < limit {
		msg, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return TradesPage{}, ctx.Err()
			}
			return TradesPage{}, fmt.Errorf("poslog trades recv: %w", err)
		}

		meta, _ := msg.Metadata()
		if meta != nil {
			lastSeq = meta.Sequence.Stream
		}

		var e Event
		if jsonErr := json.Unmarshal(msg.Data, &e); jsonErr != nil {
			if meta != nil && meta.NumPending == 0 {
				break
			}
			continue
		}

		if e.Kind == KindPositionClosed {
			var p PositionClosedPayload
			if jsonErr := json.Unmarshal(e.Payload, &p); jsonErr == nil {
				trades = append(trades, TradeRecord{
					HandID:      e.HandID,
					Symbol:      p.Symbol,
					Side:        p.Side,
					Qty:         p.Qty,
					EntryPrice:  p.EntryPrice,
					ExitPrice:   p.ClosePrice,
					EntryAt:     p.EntryAt,
					ExitAt:      e.At,
					RealizedPnL: p.RealizedPnL,
					Source:      p.ExitReason,
					Cursor:      lastSeq,
				})
			}
		}

		if meta != nil && meta.NumPending == 0 {
			break
		}
	}

	var next uint64
	hasMore := len(trades) == limit
	if hasMore && lastSeq > 0 {
		next = lastSeq + 1
	}

	return TradesPage{
		Trades:  trades,
		HasMore: hasMore,
		Next:    next,
	}, nil
}

// drain pulls all available messages matching filterSubject from the stream.
// An ordered ephemeral consumer is created and discarded after the call.
// Returns an empty slice (not error) when no messages match the filter.
func (l *NatsLog) drain(ctx context.Context, filterSubject string) ([]Event, error) {
	sub, err := l.js.SubscribeSync(filterSubject,
		nats.OrderedConsumer(),
		nats.DeliverAll(),
		nats.Context(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("poslog drain subscribe %q: %w", filterSubject, err)
	}
	defer sub.Unsubscribe()

	var events []Event
	for {
		msg, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break // no more messages
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("poslog drain recv: %w", err)
		}
		var e Event
		if jsonErr := json.Unmarshal(msg.Data, &e); jsonErr == nil {
			events = append(events, e)
		}
		meta, _ := msg.Metadata()
		if meta != nil && meta.NumPending == 0 {
			break
		}
	}
	return events, nil
}

// PurgeHand deletes all poslog messages for a hand from the HELM_POSITIONS stream.
// Uses a subject-scoped purge so only messages for this hand are removed — messages
// belonging to other hands on the same helm are unaffected.
func (l *NatsLog) PurgeHand(_ context.Context, helmID, handID string) error {
	subject := fmt.Sprintf("%s.%s.%s.>", subjectBase, helmID, handID)
	// Legacy JetStream API: *StreamPurgeRequest implements PurgeOpt via
	// configureJSContext — pass directly instead of nats.WithPurgeSubject
	// (which only exists in the newer jetstream package).
	return l.js.PurgeStream(streamName, &nats.StreamPurgeRequest{Subject: subject})
}
