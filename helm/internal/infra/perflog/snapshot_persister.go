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
	snapshotPersisterDurable = "helm-snapshots-persister"
	snapshotBatchSize        = 100
	snapshotFlushInterval    = 5 * time.Second
)

// SnapshotPersister subscribes to helm.snapshot.> on HELM_SNAPSHOTS JetStream
// and batch-inserts into equity_snapshots in PostgreSQL.
// It follows the same pattern as eventlog.Persister: explicit ack, NAK on PG failure.
type SnapshotPersister struct {
	js nats.JetStreamContext
	db *sql.DB
}

// NewSnapshotPersister creates a SnapshotPersister. Call Run() to start it.
func NewSnapshotPersister(js nats.JetStreamContext, db *sql.DB) *SnapshotPersister {
	return &SnapshotPersister{js: js, db: db}
}

// Run subscribes and batches snapshots until ctx is cancelled.
func (p *SnapshotPersister) Run(ctx context.Context) {
	msgCh := make(chan *nats.Msg, snapshotBatchSize*2)

	// DeliverAll + ON CONFLICT DO NOTHING: replays any snapshots accumulated in the
	// stream before the persister was first started, without producing duplicates.
	// MaxDeliver(-1): retry NAK'd messages indefinitely so a transient PG outage
	// does not silently send snapshots to the JetStream deadletter.
	// AckWait 30s leaves room for the batch flush (10s context) plus jitter.
	sub, err := p.js.ChanSubscribe(
		"helm.snapshot.>",
		msgCh,
		nats.Durable(snapshotPersisterDurable),
		nats.DeliverAll(),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(-1),
		nats.BindStream(snapshotStream),
	)
	if err != nil {
		slog.Error("snapshot persister: subscribe failed", "err", err)
		return
	}
	defer sub.Unsubscribe() //nolint:errcheck

	slog.Info("snapshot persister: started — helm.snapshot.> → postgres")

	ticker := time.NewTicker(snapshotFlushInterval)
	defer ticker.Stop()

	var (
		buf   []*nats.Msg
		snaps []perf.Snapshot
	)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		if err := p.insertBatch(ctx, snaps); err != nil {
			slog.Warn("snapshot persister: batch insert failed — retrying", "count", len(buf), "err", err)
			for _, m := range buf {
				_ = m.Nak()
			}
		} else {
			for _, m := range buf {
				_ = m.Ack()
			}
		}
		buf = buf[:0]
		snaps = snaps[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			slog.Info("snapshot persister: stopped")
			return

		case <-ticker.C:
			flush()

		case msg := <-msgCh:
			var s perf.Snapshot
			if err := json.Unmarshal(msg.Data, &s); err != nil {
				slog.Warn("snapshot persister: unmarshal failed — acking and skipping", "err", err)
				_ = msg.Ack()
				continue
			}
			buf = append(buf, msg)
			snaps = append(snaps, s)
			if len(buf) >= snapshotBatchSize {
				flush()
			}
		}
	}
}

func (p *SnapshotPersister) insertBatch(ctx context.Context, snaps []perf.Snapshot) error {
	if len(snaps) == 0 {
		return nil
	}

	// Build multi-row INSERT with ON CONFLICT DO NOTHING (dedup by the unique index).
	// Columns: helm_id, hand_id, ts, cash, equity, realized_pnl, unrealized_pnl, positions
	const cols = 8
	placeholders := make([]string, len(snaps))
	args := make([]any, 0, len(snaps)*cols)

	for i, s := range snaps {
		base := i * cols
		placeholders[i] = fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8)

		posJSON, _ := json.Marshal(s.Positions)
		if posJSON == nil {
			posJSON = []byte("[]")
		}

		var handID any
		if s.HandID != "" {
			handID = s.HandID
		}

		args = append(args,
			s.HelmID,
			handID,
			// Truncate to millisecond so PG dedup granularity matches the JetStream
			// MsgIdHdr (which uses TS.UnixMilli()). Otherwise two snapshots within
			// the same ms but different µs could both land in PG.
			s.TS.Truncate(time.Millisecond),
			s.Cash.String(),
			s.Equity.String(),
			s.RealizedPnL.String(),
			s.UnrealizedPnL.String(),
			posJSON,
		)
	}

	query := `INSERT INTO equity_snapshots
		(helm_id, hand_id, ts, cash, equity, realized_pnl, unrealized_pnl, positions)
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
