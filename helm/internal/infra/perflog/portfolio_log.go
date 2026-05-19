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
	portfolioStream   = "PORTFOLIO_SNAPSHOTS"
	portfolioSubjBase = "portfolio"
	portfolioMaxAge   = 90 * 24 * time.Hour
)

type jsPortfolioLog struct {
	js nats.JetStreamContext
}

// NewPortfolioLog creates the PORTFOLIO_SNAPSHOTS stream (idempotent) and returns
// a JetStream-backed perf.PortfolioLog.
//
// Subjects:
//
//	portfolio.{helm_id}           — helm-level snapshots
//	portfolio.{helm_id}.{hand_id} — hand-level snapshots
func NewPortfolioLog(js nats.JetStreamContext) (perf.PortfolioLog, error) {
	if _, err := js.StreamInfo(portfolioStream); err != nil {
		return nil, fmt.Errorf("stream %s not found: %w", portfolioStream, err)
	}
	return &jsPortfolioLog{js: js}, nil
}

func subject(helmID, handID string) string {
	if handID == "" {
		return fmt.Sprintf("%s.%s", portfolioSubjBase, helmID)
	}
	return fmt.Sprintf("%s.%s.%s", portfolioSubjBase, helmID, handID)
}

func (l *jsPortfolioLog) Append(ctx context.Context, s perf.PortfolioSnapshot) error {
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("portfolio_log marshal: %w", err)
	}
	msg := nats.NewMsg(subject(s.HelmID, s.HandID))
	msg.Data = data
	_, err = l.js.PublishMsg(msg, nats.Context(ctx))
	return err
}

func (l *jsPortfolioLog) Query(ctx context.Context, helmID, handID string, page perf.Page) (perf.PortfolioPage, error) {
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
		return perf.PortfolioPage{}, err
	}

	hasMore := len(snaps) > limit
	if hasMore {
		snaps = snaps[:limit]
	}
	var next time.Time
	if hasMore {
		next = snaps[len(snaps)-1].TS
	}
	return perf.PortfolioPage{Snapshots: snaps, Next: next, HasMore: hasMore}, nil
}

func (l *jsPortfolioLog) Latest(ctx context.Context, helmID, handID string, n int) ([]perf.PortfolioSnapshot, error) {
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

func (l *jsPortfolioLog) drain(ctx context.Context, helmID, handID string, maxMsgs int, opts []nats.SubOpt) ([]perf.PortfolioSnapshot, error) {
	subj := subject(helmID, handID)
	sub, err := l.js.SubscribeSync(subj, opts...)
	if err != nil {
		return nil, fmt.Errorf("portfolio_log subscribe %q: %w", subj, err)
	}
	defer sub.Unsubscribe()

	var snaps []perf.PortfolioSnapshot
	for {
		msg, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("portfolio_log recv: %w", err)
		}
		var s perf.PortfolioSnapshot
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
