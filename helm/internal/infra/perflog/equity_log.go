package perflog

import (
	"mallow/helm/internal/runtime/perf"

	"github.com/nats-io/nats.go"
)

// NewEquityLog is removed — equity data is now part of Snapshot (HELM_SNAPSHOTS stream).
// Deprecated: use NewSnapshotLog.
func NewEquityLog(js nats.JetStreamContext) (perf.SnapshotLog, error) {
	return NewSnapshotLog(js)
}
