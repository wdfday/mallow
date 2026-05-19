package perflog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/runtime/perf"
)

const fillStream = "TRADE_FILLS"

// FillPage is one cursor page of fill results from the TRADE_FILLS stream.
type FillPage struct {
	Fills   []natsapi.TransactionMsg `json:"fills"`
	Next    time.Time                `json:"next"`
	HasMore bool                     `json:"has_more"`
}

// FillLog queries the TRADE_FILLS JetStream stream by account_id.
type FillLog struct {
	js nats.JetStreamContext
}

func NewFillLog(js nats.JetStreamContext) (*FillLog, error) {
	if _, err := js.StreamInfo(fillStream); err != nil {
		return nil, fmt.Errorf("stream %s not found: %w", fillStream, err)
	}
	return &FillLog{js: js}, nil
}

func (l *FillLog) Query(ctx context.Context, accountID string, page perf.Page) (FillPage, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = 200
	}

	subj := fmt.Sprintf(natsapi.SubjTradeFilled, accountID)
	opts := []nats.SubOpt{nats.OrderedConsumer(), nats.Context(ctx)}
	if page.After.IsZero() {
		opts = append(opts, nats.DeliverAll())
	} else {
		opts = append(opts, nats.StartTime(page.After))
	}

	sub, err := l.js.SubscribeSync(subj, opts...)
	if err != nil {
		return FillPage{}, fmt.Errorf("fill_log subscribe %q: %w", subj, err)
	}
	defer sub.Unsubscribe()

	var fills []natsapi.TransactionMsg
	for len(fills) < limit+1 {
		msg, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return FillPage{}, ctx.Err()
			}
			return FillPage{}, fmt.Errorf("fill_log recv: %w", err)
		}
		var txn natsapi.TransactionMsg
		if jsonErr := json.Unmarshal(msg.Data, &txn); jsonErr == nil {
			fills = append(fills, txn)
		}
		meta, _ := msg.Metadata()
		if meta != nil && meta.NumPending == 0 {
			break
		}
	}

	hasMore := len(fills) > limit
	if hasMore {
		fills = fills[:limit]
	}
	var next time.Time
	if hasMore && len(fills) > 0 {
		next = fills[len(fills)-1].FilledAt
	}
	return FillPage{Fills: fills, Next: next, HasMore: hasMore}, nil
}
