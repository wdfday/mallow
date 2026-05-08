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
	_, err := js.AddStream(&nats.StreamConfig{
		Name:       streamName,
		Subjects:   []string{subjectBase + ".>"},
		Storage:    nats.FileStorage,
		MaxAge:     30 * 24 * time.Hour,
		Duplicates: 2 * time.Minute,
	})
	if err != nil {
		// Already exists — verify we can reach it.
		if _, infoErr := js.StreamInfo(streamName); infoErr != nil {
			return nil, fmt.Errorf("create stream %s: %w", streamName, err)
		}
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
