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
	"mallow/helm/internal/safe"
)

const (
	equityBatchSize     = 100
	equityFlushInterval = 5 * time.Second
	equityConsumer      = "equity-persister"
)

// EquityPersister subscribes to the HELM_EQUITY JetStream stream and
// batch-inserts helm equity snapshots into equity_snapshots (PostgreSQL).
// Replaces the old KV-based SnapshotPersister.
type EquityPersister struct {
	js nats.JetStreamContext
	db *sql.DB
}

// NewEquityPersister creates an EquityPersister. Call Run() to start it.
func NewEquityPersister(js nats.JetStreamContext, db *sql.DB) *EquityPersister {
	return &EquityPersister{js: js, db: db}
}

// Run subscribes to helm.equity.> and flushes batches to PG until ctx is cancelled.
func (p *EquityPersister) Run(ctx context.Context) {
	defer safe.Recover()
	sub, err := p.js.PullSubscribe("helm.equity.>", equityConsumer,
		nats.BindStream("HELM_EQUITY"),
		nats.AckExplicit(),
		nats.DeliverAll(),
		nats.ManualAck(),
	)
	if err != nil {
		slog.Error("equity persister: subscribe failed", "err", err)
		return
	}
	defer sub.Unsubscribe() //nolint:errcheck

	slog.Info("equity persister: started — HELM_EQUITY → postgres")

	ticker := time.NewTicker(equityFlushInterval)
	defer ticker.Stop()

	var buf []perf.Snapshot

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := p.insertBatch(ctx, buf); err != nil {
			slog.Warn("equity persister: batch insert failed", "count", len(buf), "err", err)
			return
		}
		buf = buf[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			slog.Info("equity persister: stopped")
			return

		case <-ticker.C:
			flush()
			// Pull any pending messages.
			msgs, err := sub.Fetch(equityBatchSize, nats.MaxWait(200*time.Millisecond))
			if err != nil && err != nats.ErrTimeout {
				slog.Warn("equity persister: fetch error", "err", err)
				continue
			}
			for _, msg := range msgs {
				var s perf.Snapshot
				if err := json.Unmarshal(msg.Data, &s); err != nil {
					slog.Warn("equity persister: unmarshal failed", "err", err)
					msg.Ack() //nolint:errcheck
					continue
				}
				buf = append(buf, s)
				msg.Ack() //nolint:errcheck
			}
			if len(buf) >= equityBatchSize {
				flush()
			}
		}
	}
}

func (p *EquityPersister) insertBatch(ctx context.Context, snaps []perf.Snapshot) error {
	if len(snaps) == 0 {
		return nil
	}

	const cols = 7
	placeholders := make([]string, len(snaps))
	args := make([]any, 0, len(snaps)*cols)

	for i, s := range snaps {
		base := i * cols
		placeholders[i] = fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7)

		posJSON, _ := json.Marshal(s.Positions)
		if posJSON == nil {
			posJSON = []byte("[]")
		}
		args = append(args,
			s.HelmID,
			s.TS.Truncate(time.Millisecond),
			s.Cash.String(),
			s.Equity.String(),
			s.RealizedPnL.String(),
			s.UnrealizedPnL.String(),
			posJSON,
		)
	}

	query := `INSERT INTO equity_snapshots
		(helm_id, ts, cash, equity, realized_pnl, unrealized_pnl, positions)
		VALUES ` + strings.Join(placeholders, ",") + `
		ON CONFLICT (helm_id, COALESCE(hand_id, '00000000-0000-0000-0000-000000000000'), ts) DO NOTHING`

	tCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := p.db.ExecContext(tCtx, query, args...)
	if err != nil {
		return fmt.Errorf("equity_snapshots insert: %w", err)
	}
	return nil
}
