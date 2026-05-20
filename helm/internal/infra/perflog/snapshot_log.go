package perflog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"mallow/helm/internal/runtime/perf"
)

const (
	snapshotStream   = "HELM_SNAPSHOTS"
	snapshotSubjBase = "helm.snapshot"
)

type jsSnapshotLog struct {
	js nats.JetStreamContext
}

// NewSnapshotLog returns a JetStream-backed perf.SnapshotLog.
// The HELM_SNAPSHOTS stream must already exist (created by infra/nats/nats.go).
//
// Subjects:
//
//	helm.snapshot.{helm_id}           — helm-level snapshots
//	helm.snapshot.{helm_id}.{hand_id} — hand-level snapshots
func NewSnapshotLog(js nats.JetStreamContext) (perf.SnapshotLog, error) {
	if _, err := js.StreamInfo(snapshotStream); err != nil {
		return nil, fmt.Errorf("stream %s not found: %w", snapshotStream, err)
	}
	return &jsSnapshotLog{js: js}, nil
}

func snapshotSubject(helmID, handID string) string {
	if handID == "" {
		return fmt.Sprintf("%s.%s", snapshotSubjBase, helmID)
	}
	return fmt.Sprintf("%s.%s.%s", snapshotSubjBase, helmID, handID)
}

func (l *jsSnapshotLog) Append(ctx context.Context, s perf.Snapshot) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("snapshot_log marshal: %w", err)
	}
	msg := nats.NewMsg(snapshotSubject(s.HelmID, s.HandID))
	msg.Data = data
	// Dedup by helm+hand+ts so a restart that replays snapshots doesn't duplicate.
	msg.Header.Set(nats.MsgIdHdr, fmt.Sprintf("%s-%s-%d", s.HelmID, s.HandID, s.TS.UnixMilli()))
	_, err = l.js.PublishMsg(msg, nats.Context(ctx))
	return err
}

func (l *jsSnapshotLog) Query(ctx context.Context, helmID, handID string, page perf.Page) (perf.SnapshotPage, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = 500
	}

	opts := []nats.SubOpt{nats.OrderedConsumer(), nats.Context(ctx)}
	if page.After.IsZero() {
		opts = append(opts, nats.DeliverAll())
	} else {
		opts = append(opts, nats.StartTime(page.After))
	}

	snaps, err := l.drain(ctx, helmID, handID, limit+1, opts)
	if err != nil {
		return perf.SnapshotPage{}, err
	}

	hasMore := len(snaps) > limit
	if hasMore {
		snaps = snaps[:limit]
	}
	var next time.Time
	if hasMore {
		next = snaps[len(snaps)-1].TS
	}
	return perf.SnapshotPage{Snapshots: snaps, Next: next, HasMore: hasMore}, nil
}

func (l *jsSnapshotLog) Latest(ctx context.Context, helmID, handID string, n int) ([]perf.Snapshot, error) {
	if n <= 0 {
		n = 100
	}
	all, err := l.drain(ctx, helmID, handID, 0, []nats.SubOpt{
		nats.OrderedConsumer(),
		nats.DeliverAll(),
		nats.Context(ctx),
	})
	if err != nil {
		return nil, err
	}
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

func (l *jsSnapshotLog) drain(ctx context.Context, helmID, handID string, maxMsgs int, opts []nats.SubOpt) ([]perf.Snapshot, error) {
	subj := snapshotSubject(helmID, handID)
	sub, err := l.js.SubscribeSync(subj, opts...)
	if err != nil {
		return nil, fmt.Errorf("snapshot_log subscribe %q: %w", subj, err)
	}
	defer sub.Unsubscribe()

	var snaps []perf.Snapshot
	for {
		msg, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("snapshot_log recv: %w", err)
		}
		var s perf.Snapshot
		if jsonErr := json.Unmarshal(msg.Data, &s); jsonErr == nil {
			snaps = append(snaps, s)
		}
		meta, _ := msg.Metadata()
		if meta != nil && meta.NumPending == 0 {
			break
		}
		if maxMsgs > 0 && len(snaps) >= maxMsgs {
			break
		}
	}
	return snaps, nil
}
