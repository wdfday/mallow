// Package perflog provides JetStream-backed implementations of perf.EquityLog
// and perf.TradeLog. Each hand gets its own subject so replays are filtered by
// hand_id without scanning the entire stream.
//
// Stream layout:
//
//	HELM_EQUITY  subjects: helm.equity.>   — equity snapshots per bar close
//	HELM_TRADES  subjects: helm.trades.>   — closed round-trip trades
//
// Both streams use FileStorage and a dedup window so bursts / restarts
// cannot produce duplicate entries within the window.
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
	equityStream   = "HELM_EQUITY"
	equitySubjBase = "helm.equity"
	equityMaxAge   = 90 * 24 * time.Hour
	equityDedupWin = 5 * time.Minute
)

type jsEquityLog struct {
	js nats.JetStreamContext
}

// NewEquityLog creates the HELM_EQUITY stream (idempotent) and returns a
// JetStream-backed perf.EquityLog.
func NewEquityLog(js nats.JetStreamContext) (perf.EquityLog, error) {
	_, err := js.AddStream(&nats.StreamConfig{
		Name:       equityStream,
		Subjects:   []string{equitySubjBase + ".>"},
		Storage:    nats.FileStorage,
		MaxAge:     equityMaxAge,
		Duplicates: equityDedupWin,
	})
	if err != nil {
		if _, infoErr := js.StreamInfo(equityStream); infoErr != nil {
			return nil, fmt.Errorf("create stream %s: %w", equityStream, err)
		}
	}
	return &jsEquityLog{js: js}, nil
}

func (l *jsEquityLog) Append(ctx context.Context, p perf.EquityLogPoint) error {
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("equity_log marshal: %w", err)
	}
	msg := nats.NewMsg(fmt.Sprintf("%s.%s", equitySubjBase, p.HandID))
	msg.Data = data
	msg.Header.Set(nats.MsgIdHdr, fmt.Sprintf("%s-%d", p.HandID, p.TS.UnixMilli()))
	_, err = l.js.PublishMsg(msg, nats.Context(ctx))
	return err
}

func (l *jsEquityLog) AppendBatch(ctx context.Context, pts []perf.EquityLogPoint) error {
	for _, p := range pts {
		if err := l.Append(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (l *jsEquityLog) Query(ctx context.Context, handID string, page perf.Page) (perf.EquityLogPage, error) {
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

	pts, err := l.drainEquity(ctx, handID, limit+1, opts)
	if err != nil {
		return perf.EquityLogPage{}, err
	}

	hasMore := len(pts) > limit
	if hasMore {
		pts = pts[:limit]
	}
	var next time.Time
	if hasMore {
		next = pts[len(pts)-1].TS
	}
	return perf.EquityLogPage{Points: pts, Next: next, HasMore: hasMore}, nil
}

func (l *jsEquityLog) Latest(ctx context.Context, handID string, n int) ([]perf.EquityLogPoint, error) {
	if n <= 0 {
		n = 100
	}
	// Drain all and return tail — acceptable since JetStream sequential reads are fast
	// and equity series per hand are typically O(10k) points.
	all, err := l.drainEquity(ctx, handID, 0, []nats.SubOpt{
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

func (l *jsEquityLog) drainEquity(ctx context.Context, handID string, maxMsgs int, opts []nats.SubOpt) ([]perf.EquityLogPoint, error) {
	subj := fmt.Sprintf("%s.%s", equitySubjBase, handID)
	sub, err := l.js.SubscribeSync(subj, opts...)
	if err != nil {
		return nil, fmt.Errorf("equity_log subscribe %q: %w", subj, err)
	}
	defer sub.Unsubscribe()

	var pts []perf.EquityLogPoint
	for {
		msg, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("equity_log recv: %w", err)
		}
		var p perf.EquityLogPoint
		if jsonErr := json.Unmarshal(msg.Data, &p); jsonErr == nil {
			pts = append(pts, p)
		}
		meta, _ := msg.Metadata()
		if meta != nil && meta.NumPending == 0 {
			break
		}
		if maxMsgs > 0 && len(pts) >= maxMsgs {
			break
		}
	}
	return pts, nil
}
