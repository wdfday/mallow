package perf

import "time"

// EquityLog is removed — equity is now embedded in Snapshot (HELM_SNAPSHOTS stream).
// Deprecated: EquityLogPoint kept for any callers still referencing it.
type EquityLogPoint = Snapshot

// EquityLogPage is removed — use SnapshotPage.
// Deprecated: kept as an alias.
type EquityLogPage = SnapshotPage

// EquityLog was the append-only store for per-hand equity curve points.
// Removed: its functionality is merged into SnapshotLog. This interface alias
// preserves the type for callers that have not yet migrated.
//
// Deprecated: use SnapshotLog.
type EquityLog = SnapshotLog

// Page is a cursor-based page request used by both SnapshotLog and TradeLog.
// After=zero value means from the beginning.
// Limit=0 defaults to a reasonable page size (implementation-defined).
type Page struct {
	After time.Time // exclusive lower bound on TS; zero = from start
	Limit int
}
