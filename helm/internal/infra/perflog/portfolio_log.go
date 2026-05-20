package perflog

import (
	"mallow/helm/internal/runtime/perf"

	"github.com/nats-io/nats.go"
)

// NewPortfolioLog is replaced by NewSnapshotLog (HELM_SNAPSHOTS stream).
// Deprecated: use NewSnapshotLog.
func NewPortfolioLog(js nats.JetStreamContext) (perf.SnapshotLog, error) {
	return NewSnapshotLog(js)
}
