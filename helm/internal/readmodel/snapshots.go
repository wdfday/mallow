package readmodel

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// SnapshotRow is one raw snapshot row from `equity_snapshots`. Lossless mirror of the
// table schema (positions JSONB is exposed as raw bytes — callers decode when needed).
type SnapshotRow struct {
	HelmID        uuid.UUID
	HandID        *uuid.UUID // nil for helm-level snapshots
	TS            time.Time
	Cash          decimal.Decimal
	Equity        decimal.Decimal
	RealizedPnL   decimal.Decimal
	UnrealizedPnL decimal.Decimal
	PositionsJSON []byte
}
