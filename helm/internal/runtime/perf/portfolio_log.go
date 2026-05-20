package perf

// PortfolioSnapshot and PortfolioLog are replaced by Snapshot and SnapshotLog.
// Kept as type aliases during migration; remove after all callers are updated.

// PortfolioSnapshot is an alias for Snapshot for backward compatibility.
// Deprecated: use Snapshot directly.
type PortfolioSnapshot = Snapshot

// PortfolioLog is an alias for SnapshotLog for backward compatibility.
// Deprecated: use SnapshotLog directly.
type PortfolioLog = SnapshotLog

// PortfolioPage is an alias for SnapshotPage for backward compatibility.
// Deprecated: use SnapshotPage directly.
type PortfolioPage = SnapshotPage
