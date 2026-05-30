package perflog
// snapshot_log.go removed — KV-backed SnapshotLog replaced by SnapshotWorker + EquityPersister.
// Helm equity snapshots now flow: MarkSnapshotDirty → SnapshotWorker → helm.equity.{id} JetStream → EquityPersister → equity_snapshots PG.
