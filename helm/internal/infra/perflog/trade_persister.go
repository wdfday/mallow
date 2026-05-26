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

	"mallow/helm/internal/runtime/perf"
)

const (
	tradePersisterDurable = "helm-trades-persister"
	tradeBatchSize        = 100
	tradeFlushInterval    = 5 * time.Second
)

// TradePersister subscribes to helm.trades.> on HELM_TRADES JetStream and batch-inserts
// completed round-trip trades into the PostgreSQL `trades` table.
//
// Mirrors SnapshotPersister: DeliverAll + ON CONFLICT DO NOTHING for idempotent
// replay, MaxDeliver(-1) + AckWait(30s) so a transient PG outage doesn't push
// trades into the JetStream deadletter.
type TradePersister struct {
	js nats.JetStreamContext
	db *sql.DB
}

// NewTradePersister wires the JetStream subscriber. Call Run() to start the loop.
func NewTradePersister(js nats.JetStreamContext, db *sql.DB) *TradePersister {
	return &TradePersister{js: js, db: db}
}

// Run consumes helm.trades.> and batches INSERTs until ctx is cancelled.
func (p *TradePersister) Run(ctx context.Context) {
	msgCh := make(chan *nats.Msg, tradeBatchSize*2)

	sub, err := p.js.ChanSubscribe(
		"helm.trades.>",
		msgCh,
		nats.Durable(tradePersisterDurable),
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(-1),
		nats.BindStream(tradeStream),
	)
	if err != nil {
		slog.Error("trade persister: subscribe failed", "err", err)
		return
	}
	defer sub.Unsubscribe() //nolint:errcheck

	slog.Info("trade persister: started — helm.trades.> → postgres")

	ticker := time.NewTicker(tradeFlushInterval)
	defer ticker.Stop()

	var (
		buf  []*nats.Msg
		recs []perf.TradeRecord
	)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := p.insertBatch(ctx, recs); err != nil {
			slog.Warn("trade persister: batch insert failed — NAK for retry",
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
			slog.Info("trade persister: stopped")
			return

		case <-ticker.C:
			flush()

		case msg := <-msgCh:
			var t perf.TradeRecord
			if err := json.Unmarshal(msg.Data, &t); err != nil {
				slog.Warn("trade persister: unmarshal failed — acking and skipping", "err", err)
				_ = msg.Ack()
				continue
			}
			buf = append(buf, msg)
			recs = append(recs, t)
			if len(buf) >= tradeBatchSize {
				flush()
			}
		}
	}
}

// insertBatch INSERTs the batch into the trades table.
// Columns map 1:1 with TradeRecord. net_pnl, holding_seconds, r_multiple are
// computed by PG GENERATED columns.
func (p *TradePersister) insertBatch(ctx context.Context, recs []perf.TradeRecord) error {
	if len(recs) == 0 {
		return nil
	}

	const cols = 23
	placeholders := make([]string, len(recs))
	args := make([]any, 0, len(recs)*cols)

	for i, r := range recs {
		base := i * cols
		placeholders[i] = "(" + buildPlaceholders(base+1, cols) + ")"

		args = append(args,
			r.HelmID,
			r.HandID,
			r.UserID,
			r.Symbol,
			r.Side,
			nullableText(r.Timeframe),
			nullableNumeric(r.EntryPrice),
			nullableNumeric(r.ExitPrice),
			nullableNumeric(r.Qty),
			nullableNumeric(r.GrossPnL),
			coalesceNumeric(r.Commission),
			nullableNumeric(r.StopLossPrice),
			nullableNumeric(r.TakeProfitPrice),
			nullableNumeric(r.PlannedRisk),
			nullableText(r.EntryOrderID),
			nullableText(r.ExitOrderID),
			nullableInt(r.NEntries),
			nullableText(r.ExitReason),
			nullableText(r.Strategy),
			nullableText(r.PatternKind),
			nullableText(r.RegimeState),
			nullableTime(r.EntryAt),
		)
		// exit_at appended below as the final column (separate from EntryAt for clarity).
		args = append(args, r.ExitAt)
	}

	query := `INSERT INTO trades
		(helm_id, hand_id, user_id, symbol, side, timeframe,
		 entry_price, exit_price, qty, pnl, commission,
		 stop_loss_price, take_profit_price, planned_risk,
		 entry_order_id, exit_order_id, n_entries,
		 source, strategy, pattern_kind, regime_state,
		 entry_at, exit_at)
		VALUES ` + strings.Join(placeholders, ",") + `
		ON CONFLICT (hand_id, entry_at, exit_at) DO NOTHING`

	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := p.db.ExecContext(tCtx, query, args...); err != nil {
		return fmt.Errorf("trades insert: %w", err)
	}
	return nil
}

// buildPlaceholders returns "$start,$start+1,…,$start+n-1".
func buildPlaceholders(start, n int) string {
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(parts, ",")
}

// nullableNumeric returns nil for an empty string so PG NUMERIC stays NULL,
// preserving "no value reported" vs "explicitly zero".
func nullableNumeric(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// coalesceNumeric forces "" to "0" — used for commission which is NOT NULL in the schema.
func coalesceNumeric(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func nullableInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
