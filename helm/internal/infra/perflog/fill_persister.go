package perflog

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
)

const (
	fillPersisterDurable = "helm-fills-persister"
	fillBatchSize        = 200
	fillFlushInterval    = 3 * time.Second
)

// FillPersister drains TRADE_FILLS JetStream → PostgreSQL `fills`.
// Only kind="fill" events are persisted; open_order and cancel are acked and skipped.
type FillPersister struct {
	js nats.JetStreamContext
	db *sql.DB
}

func NewFillPersister(js nats.JetStreamContext, db *sql.DB) *FillPersister {
	return &FillPersister{js: js, db: db}
}

func (p *FillPersister) Run(ctx context.Context) {
	msgCh := make(chan *nats.Msg, fillBatchSize*2)

	sub, err := p.js.ChanSubscribe(
		"trade.filled.>",
		msgCh,
		nats.Durable(fillPersisterDurable),
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(-1),
		nats.BindStream(fillStream),
	)
	if err != nil {
		slog.Error("fill persister: subscribe failed", "err", err)
		return
	}
	defer func(sub *nats.Subscription) {
		err := sub.Unsubscribe()
		if err != nil {

		}
	}(sub) //nolint:errcheck

	slog.Info("fill persister: started — trade.filled.> → postgres")

	ticker := time.NewTicker(fillFlushInterval)
	defer ticker.Stop()

	var (
		buf  []*nats.Msg
		recs []natsapi.TransactionMsg
	)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := p.insertBatch(ctx, recs); err != nil {
			slog.Warn("fill persister: batch insert failed — NAK for retry",
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
			slog.Info("fill persister: stopped")
			return
		case <-ticker.C:
			flush()
		case msg := <-msgCh:
			var txn natsapi.TransactionMsg
			if err := json.Unmarshal(msg.Data, &txn); err != nil {
				slog.Warn("fill persister: unmarshal failed — skipping", "err", err)
				_ = msg.Ack()
				continue
			}
			if txn.Kind != "fill" {
				_ = msg.Ack()
				continue
			}
			buf = append(buf, msg)
			recs = append(recs, txn)
			if len(buf) >= fillBatchSize {
				flush()
			}
		}
	}
}

func (p *FillPersister) insertBatch(ctx context.Context, recs []natsapi.TransactionMsg) error {
	if len(recs) == 0 {
		return nil
	}

	const cols = 13
	placeholders := make([]string, len(recs))
	args := make([]any, 0, len(recs)*cols)

	for i, r := range recs {
		placeholders[i] = "(" + buildPlaceholders(i*cols+1, cols) + ")"
		args = append(args,
			r.TradeID,
			nullableText(r.OrderID),
			r.HelmID,
			r.AccountID,
			r.UserID,
			nullableUUID(r.HandID),
			r.Symbol,
			r.Side,
			r.Kind,
			nullableNumeric(r.Qty.String()),
			nullableNumeric(r.AvgPrice.String()),
			nullableNumeric(r.Fee.String()),
			r.FilledAt,
		)
	}

	query := `INSERT INTO fills
		(trade_id, order_id, helm_id, account_id, user_id, hand_id,
		 symbol, side, kind, qty, avg_price, fee, filled_at)
		VALUES ` + strings.Join(placeholders, ",") + `
		ON CONFLICT (trade_id) DO NOTHING`

	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := p.db.ExecContext(tCtx, query, args...); err != nil {
		return fmt.Errorf("fills insert: %w", err)
	}
	return nil
}
