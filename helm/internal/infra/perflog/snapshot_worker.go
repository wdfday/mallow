package perflog

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"mallow/helm/internal/runtime/perf"
)

const (
	// snapshotDebounce is how quickly the worker flushes after a fill hint.
	// Fills call HelmRuntime.MarkSnapshotDirty(); the worker emits within this window.
	snapshotDebounce = 500 * time.Millisecond

	// snapshotHeartbeat is the unconditional emit interval regardless of dirty flags.
	// Catches unrealized-PnL drift and any state changes not driven by fills.
	snapshotHeartbeat = 60 * time.Second
)

// SnapshotSource enumerates runtimes for the SnapshotWorker. Registry implements this.
type SnapshotSource interface {
	// DirtyRuntimes returns runtimes marked dirty since the last call and atomically
	// clears their dirty flags, so each fill burst is emitted exactly once.
	DirtyRuntimes() []SnapshotEmitter

	// AllRuntimes returns every live runtime for the unconditional heartbeat sweep.
	AllRuntimes() []SnapshotEmitter
}

// SnapshotEmitter is the narrow interface required of each HelmRuntime.
type SnapshotEmitter interface {
	HelmStringID() string
	BuildSnapshot(ts time.Time) *perf.Snapshot
}

// SnapshotWorker is the single background goroutine that writes helm equity
// snapshots to the HELM_EQUITY JetStream stream.
//
// Design:
//   - Fills call MarkSnapshotDirty() — a single atomic store, zero I/O.
//   - Every 500ms the worker sweeps dirty runtimes and publishes their snapshots.
//   - Every 60s it sweeps ALL runtimes unconditionally (heartbeat / catch-all).
//   - One goroutine total regardless of how many helms are active.
type SnapshotWorker struct {
	src SnapshotSource
	js  nats.JetStreamContext
}

// NewSnapshotWorker creates a worker backed by src. Call Run() to start it.
func NewSnapshotWorker(src SnapshotSource, js nats.JetStreamContext) *SnapshotWorker {
	return &SnapshotWorker{src: src, js: js}
}

// Run starts the debounce and heartbeat loops. Blocks until ctx is cancelled.
// Intended to run in a dedicated goroutine via the app lifecycle.
func (w *SnapshotWorker) Run(ctx context.Context) {
	debounce := time.NewTicker(snapshotDebounce)
	heartbeat := time.NewTicker(snapshotHeartbeat)
	defer debounce.Stop()
	defer heartbeat.Stop()

	slog.Info("snapshot_worker: started", "debounce", snapshotDebounce, "heartbeat", snapshotHeartbeat)

	for {
		select {
		case <-debounce.C:
			rts := w.src.DirtyRuntimes()
			for _, rt := range rts {
				w.publish(ctx, rt)
			}

		case <-heartbeat.C:
			rts := w.src.AllRuntimes()
			for _, rt := range rts {
				w.publish(ctx, rt)
			}
			slog.Debug("snapshot_worker: heartbeat", "helms", len(rts))

		case <-ctx.Done():
			slog.Info("snapshot_worker: stopped")
			return
		}
	}
}

func (w *SnapshotWorker) publish(ctx context.Context, rt SnapshotEmitter) {
	snap := rt.BuildSnapshot(time.Now().UTC())
	if snap == nil {
		return
	}
	data, err := json.Marshal(snap)
	if err != nil {
		slog.Warn("snapshot_worker: marshal failed", "helm_id", rt.HelmStringID(), "err", err)
		return
	}
	subject := "helm.equity." + rt.HelmStringID()
	if _, err := w.js.Publish(subject, data, nats.Context(ctx)); err != nil {
		slog.Warn("snapshot_worker: publish failed", "helm_id", rt.HelmStringID(), "err", err)
	}
}
