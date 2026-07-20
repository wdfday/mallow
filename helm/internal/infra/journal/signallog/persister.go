package signallog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"mallow/helm/internal/infra/natsapi"
	"mallow/helm/internal/safe"
)

const (
	signalPersisterDurable = "helm-signals-persister"
	signalBatchSize        = 200
	signalFlushInterval    = 3 * time.Second
)

// SignalPersister drains HELM_SIGNALS JetStream → PostgreSQL `signals`.
type SignalPersister struct {
	js nats.JetStreamContext
	db *sql.DB
}

func NewSignalPersister(js nats.JetStreamContext, db *sql.DB) *SignalPersister {
	return &SignalPersister{js: js, db: db}
}

func (p *SignalPersister) Run(ctx context.Context) {
	defer safe.Recover()
	msgCh := make(chan *nats.Msg, signalBatchSize*2)

	sub, err := p.js.ChanSubscribe(
		"helm.signals.>",
		msgCh,
		nats.Durable(signalPersisterDurable),
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(-1),
		nats.BindStream(signalStream),
	)
	if err != nil {
		slog.Error("signal persister: subscribe failed", "err", err)
		return
	}
	defer func(sub *nats.Subscription) {
		_ = sub.Unsubscribe()
	}(sub)

	slog.Info("signal persister: started — helm.signals.> → postgres")

	ticker := time.NewTicker(signalFlushInterval)
	defer ticker.Stop()

	var (
		buf  []*nats.Msg
		recs []natsapi.SignalMsg
	)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := p.insertBatch(ctx, recs); err != nil {
			slog.Warn("signal persister: batch insert failed — NAK for retry",
				"count", len(buf), "err", err)
			for _, m := range buf {
				_ = m.Nak()
			}
		} else {
			for _, m := range buf {
				_ = m.Ack()
			}
		}
		buf = buf[:0]
		recs = recs[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			slog.Info("signal persister: stopped")
			return
		case <-ticker.C:
			flush()
		case msg := <-msgCh:
			var sig natsapi.SignalMsg
			if err := json.Unmarshal(msg.Data, &sig); err != nil {
				slog.Warn("signal persister: unmarshal failed — skipping", "err", err)
				_ = msg.Ack()
				continue
			}
			buf = append(buf, msg)
			recs = append(recs, sig)
			if len(buf) >= signalBatchSize {
				flush()
			}
		}
	}
}

func (p *SignalPersister) insertBatch(ctx context.Context, recs []natsapi.SignalMsg) error {
	if len(recs) == 0 {
		return nil
	}

	const cols = 16
	placeholders := make([]string, len(recs))
	args := make([]any, 0, len(recs)*cols)

	for i, r := range recs {
		placeholders[i] = "(" + buildPlaceholders(i*cols+1, cols) + ")"
		args = append(args,
			r.ID,
			r.HelmID,
			r.HandID,
			r.UserID,
			r.Symbol,
			r.Direction,
			nullableText(r.ExitKind),
			r.Strength,
			nullableNumeric(r.Price),
			nullableNumeric(r.TargetPrice),
			nullableNumeric(r.StopPrice),
			r.IsOffset,
			nullableNumeric(r.ATR),
			nullableText(r.Reason),
			nullableTime(r.GeneratedAt),
			nullableTime(r.ReceivedAt),
		)
	}

	query := `INSERT INTO signals
		(id, helm_id, hand_id, user_id, symbol, direction, exit_kind, strength,
		 price, target_price, stop_price, is_offset, atr, reason, generated_at, received_at)
		VALUES ` + strings.Join(placeholders, ",") + `
		ON CONFLICT (id) DO NOTHING`

	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := p.db.ExecContext(tCtx, query, args...); err != nil {
		return fmt.Errorf("signals insert: %w", err)
	}
	return nil
}
