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
	tradeStream   = "HELM_TRADES"
	tradeSubjBase = "helm.trades"
	tradeMaxAge   = 365 * 24 * time.Hour
	tradeDedupWin = 30 * time.Minute
)

type jsTradeLog struct {
	js nats.JetStreamContext
}

// NewTradeLog creates the HELM_TRADES stream (idempotent) and returns a
// JetStream-backed perf.TradeLog.
func NewTradeLog(js nats.JetStreamContext) (perf.TradeLog, error) {
	if _, err := js.StreamInfo(tradeStream); err != nil {
		return nil, fmt.Errorf("stream %s not found: %w", tradeStream, err)
	}
	return &jsTradeLog{js: js}, nil
}

func (l *jsTradeLog) Append(ctx context.Context, t perf.TradeRecord) error {
	data, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("trade_log marshal: %w", err)
	}
	msg := nats.NewMsg(fmt.Sprintf("%s.%s", tradeSubjBase, t.HandID))
	msg.Data = data
	// Dedup key: hand + entry + exit timestamps — natural round-trip identity.
	msg.Header.Set(nats.MsgIdHdr, fmt.Sprintf("%s-%d-%d",
		t.HandID, t.EntryTS.UnixMilli(), t.ExitTS.UnixMilli()))
	_, err = l.js.PublishMsg(msg, nats.Context(ctx))
	return err
}

func (l *jsTradeLog) AppendBatch(ctx context.Context, trades []perf.TradeRecord) error {
	for _, t := range trades {
		if err := l.Append(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (l *jsTradeLog) Query(ctx context.Context, handID string, page perf.Page) (perf.TradeLogPage, error) {
	limit := page.Limit
	if limit <= 0 {
		limit = 200
	}

	opts := []nats.SubOpt{nats.OrderedConsumer(), nats.Context(ctx)}
	if page.After.IsZero() {
		opts = append(opts, nats.DeliverAll())
	} else {
		opts = append(opts, nats.StartTime(page.After))
	}

	trades, err := l.drainTrades(ctx, handID, limit+1, opts)
	if err != nil {
		return perf.TradeLogPage{}, err
	}

	hasMore := len(trades) > limit
	if hasMore {
		trades = trades[:limit]
	}
	var next time.Time
	if hasMore {
		next = trades[len(trades)-1].ExitTS
	}
	return perf.TradeLogPage{Trades: trades, Next: next, HasMore: hasMore}, nil
}

func (l *jsTradeLog) Since(ctx context.Context, handID string, after time.Time) ([]perf.TradeRecord, error) {
	opts := []nats.SubOpt{nats.OrderedConsumer(), nats.Context(ctx)}
	if after.IsZero() {
		opts = append(opts, nats.DeliverAll())
	} else {
		opts = append(opts, nats.StartTime(after))
	}
	return l.drainTrades(ctx, handID, 0, opts)
}

func (l *jsTradeLog) drainTrades(ctx context.Context, handID string, maxMsgs int, opts []nats.SubOpt) ([]perf.TradeRecord, error) {
	subj := fmt.Sprintf("%s.%s", tradeSubjBase, handID)
	sub, err := l.js.SubscribeSync(subj, opts...)
	if err != nil {
		return nil, fmt.Errorf("trade_log subscribe %q: %w", subj, err)
	}
	defer sub.Unsubscribe()

	var trades []perf.TradeRecord
	for {
		msg, err := sub.NextMsg(500 * time.Millisecond)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				break
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("trade_log recv: %w", err)
		}
		var t perf.TradeRecord
		if jsonErr := json.Unmarshal(msg.Data, &t); jsonErr == nil {
			trades = append(trades, t)
		}
		meta, _ := msg.Metadata()
		if meta != nil && meta.NumPending == 0 {
			break
		}
		if maxMsgs > 0 && len(trades) >= maxMsgs {
			break
		}
	}
	return trades, nil
}
