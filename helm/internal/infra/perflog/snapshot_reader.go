package perflog

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/module/analytics/domain"
	"mallow/helm/internal/readmodel"
)

// SnapshotReader reads from the PostgreSQL `equity_snapshots` table.
// Canonical source for analytical snapshot queries. Writes still go through
// JetStream → SnapshotPersister, but readers should always come here.
type SnapshotReader interface {
	// EquityCurve returns a forward-filled equity curve at the requested resolution.
	// hand_id == NULL identifies the helm-level snapshot stream;
	// non-NULL filters to a single hand.
	EquityCurve(ctx context.Context, helmID uuid.UUID, handID *uuid.UUID, from, to time.Time, res domain.Resolution) ([]domain.EquityPoint, error)

	// List returns raw snapshot rows ordered ts DESC. Unlike EquityCurve, no
	// bucketing or forward-fill — useful for audit / debug views.
	List(ctx context.Context, helmID uuid.UUID, handID *uuid.UUID, before time.Time, limit int) ([]readmodel.SnapshotRow, error)

	// Latest returns the most recent snapshot at-or-before `at`.
	// Used for "current equity" displays without scanning the full curve.
	Latest(ctx context.Context, helmID uuid.UUID, handID *uuid.UUID, at time.Time) (*domain.EquityPoint, error)

	// PersisterLag returns the staleness of the persister: the time delta between
	// now and the most recent row's ts. Used by analytics.Metadata.
	PersisterLag(ctx context.Context, helmID uuid.UUID) (time.Duration, error)
}

type pgSnapshotReader struct {
	db *sql.DB
}

// NewSnapshotReader returns a PG-backed SnapshotReader.
func NewSnapshotReader(db *sql.DB) SnapshotReader {
	return &pgSnapshotReader{db: db}
}

// EquityCurve implements forward-fill on top of the event-driven snapshot table.
//
// Query strategy:
//
//  1. generate_series produces an even-spaced bucket grid for [from, to]
//  2. LATERAL subquery picks the latest snapshot ≤ bucket time for the entity
//  3. Empty leading buckets (before the very first snapshot) collapse to NULL → caller can drop
//
// Idempotent + cheap thanks to idx_equity_snapshots_helm_ts / idx_equity_snapshots_hand_ts.
func (r *pgSnapshotReader) EquityCurve(
	ctx context.Context,
	helmID uuid.UUID,
	handID *uuid.UUID,
	from, to time.Time,
	res domain.Resolution,
) ([]domain.EquityPoint, error) {
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.AddDate(0, 0, -30)
	}
	interval := res.SQL()

	// hand_id matching uses IS NOT DISTINCT FROM so a nil handID matches NULL rows.
	var handArg any
	if handID != nil {
		handArg = *handID
	}

	rows, err := r.db.QueryContext(ctx, `
		WITH buckets AS (
			SELECT generate_series(
				date_trunc('minute', $1::timestamptz),
				date_trunc('minute', $2::timestamptz),
				$3::interval
			) AS ts
		)
		SELECT b.ts,
		       s.equity, s.cash, s.realized_pnl, s.unrealized_pnl
		FROM buckets b
		LEFT JOIN LATERAL (
			SELECT equity, cash, realized_pnl, unrealized_pnl
			FROM equity_snapshots
			WHERE helm_id = $4
			  AND hand_id IS NOT DISTINCT FROM $5
			  AND ts <= b.ts
			ORDER BY ts DESC
			LIMIT 1
		) s ON TRUE
		ORDER BY b.ts ASC
	`, from, to, interval, helmID, handArg)
	if err != nil {
		return nil, fmt.Errorf("equity_curve query: %w", err)
	}
	defer rows.Close()

	out := make([]domain.EquityPoint, 0, 256)
	for rows.Next() {
		var p domain.EquityPoint
		var equity, cash, realized, unrealized sql.NullString
		if err := rows.Scan(&p.TS, &equity, &cash, &realized, &unrealized); err != nil {
			return nil, err
		}
		// Empty buckets before the first snapshot — keep them with zeros so the
		// FE can render gaps consistently; alternatively the caller can filter.
		p.Equity = decimalFromNS(equity)
		p.Cash = decimalFromNS(cash)
		p.RealizedPnL = decimalFromNS(realized)
		p.UnrealizedPnL = decimalFromNS(unrealized)
		out = append(out, p)
	}
	return out, rows.Err()
}

// List returns raw snapshot rows newest-first, optionally bounded by `before`.
// Used by /snapshots endpoint for audit views (no bucketing).
func (r *pgSnapshotReader) List(
	ctx context.Context,
	helmID uuid.UUID,
	handID *uuid.UUID,
	before time.Time,
	limit int,
) ([]readmodel.SnapshotRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var handArg any
	if handID != nil {
		handArg = *handID
	}
	// hand_id IS NOT DISTINCT FROM $2 lets us match NULL rows for helm-level
	// snapshots when handArg is nil.
	args := []any{helmID, handArg, limit}
	query := `SELECT helm_id, hand_id, ts, cash, equity, realized_pnl, unrealized_pnl, positions
		FROM equity_snapshots
		WHERE helm_id = $1
		  AND hand_id IS NOT DISTINCT FROM $2`
	if !before.IsZero() {
		query += ` AND ts < $4`
		args = append(args, before)
	}
	query += ` ORDER BY ts DESC LIMIT $3`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("snapshot list: %w", err)
	}
	defer rows.Close()

	out := make([]readmodel.SnapshotRow, 0, limit)
	for rows.Next() {
		var s readmodel.SnapshotRow
		var handIDNS sql.NullString
		var cash, equity, realized, unrealized sql.NullString
		if err := rows.Scan(&s.HelmID, &handIDNS, &s.TS, &cash, &equity, &realized, &unrealized, &s.PositionsJSON); err != nil {
			return nil, err
		}
		if handIDNS.Valid {
			if hid, perr := uuid.Parse(handIDNS.String); perr == nil {
				s.HandID = &hid
			}
		}
		s.Cash = decimalFromNS(cash)
		s.Equity = decimalFromNS(equity)
		s.RealizedPnL = decimalFromNS(realized)
		s.UnrealizedPnL = decimalFromNS(unrealized)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *pgSnapshotReader) Latest(
	ctx context.Context,
	helmID uuid.UUID,
	handID *uuid.UUID,
	at time.Time,
) (*domain.EquityPoint, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	var handArg any
	if handID != nil {
		handArg = *handID
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT ts, equity, cash, realized_pnl, unrealized_pnl
		FROM equity_snapshots
		WHERE helm_id = $1
		  AND hand_id IS NOT DISTINCT FROM $2
		  AND ts <= $3
		ORDER BY ts DESC
		LIMIT 1
	`, helmID, handArg, at)

	var p domain.EquityPoint
	var equity, cash, realized, unrealized sql.NullString
	if err := row.Scan(&p.TS, &equity, &cash, &realized, &unrealized); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.Equity = decimalFromNS(equity)
	p.Cash = decimalFromNS(cash)
	p.RealizedPnL = decimalFromNS(realized)
	p.UnrealizedPnL = decimalFromNS(unrealized)
	return &p, nil
}

func (r *pgSnapshotReader) PersisterLag(ctx context.Context, helmID uuid.UUID) (time.Duration, error) {
	var maxTS sql.NullTime
	err := r.db.QueryRowContext(ctx,
		`SELECT MAX(ts) FROM equity_snapshots WHERE helm_id = $1`, helmID,
	).Scan(&maxTS)
	if err != nil {
		return 0, err
	}
	if !maxTS.Valid {
		return 0, nil // no data yet
	}
	return time.Since(maxTS.Time), nil
}

func decimalFromNS(ns sql.NullString) decimal.Decimal {
	if !ns.Valid {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(ns.String)
	if err != nil {
		return decimal.Zero
	}
	return d
}
