// Package service implements the analytics read layer.
//
// Architecture notes (see docs/metrics-and-reports.md):
//
//   - All queries hit PostgreSQL — `trades` for closed round-trips and
//     `equity_snapshots` for the equity curve. JetStream is the durable
//     buffer behind these tables; persister drains it asynchronously.
//   - There is no JetStream-fallback path: the lag (≤5s typical) is
//     surfaced in Metadata so the FE can render a freshness indicator.
//   - Ownership is enforced at every method via Scope.UserID; the SQL
//     filter always pins user_id so a poisoned UUID can't leak across users.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/perflog"
	"mallow/helm/internal/infra/tradelog"
	"mallow/helm/internal/module/analytics/domain"
)

// Service is the read-only analytics API. Handlers call here instead of
// directly hitting tradelog / snapshot readers so:
//   - request validation is centralised (scope, periods)
//   - response Metadata (lag, source) is uniform
//   - swapping the storage backend later requires no handler changes
type Service struct {
	trades    tradelog.Log
	snapshots perflog.SnapshotReader
	statsDB   StatsRunner
}

// StatsRunner exposes the raw SQL aggregations needed by ComputeStats.
// Separated so the service can be tested with a mocked PG layer.
type StatsRunner interface {
	RunStats(ctx context.Context, scope domain.Scope, p domain.Period) (domain.Stats, error)
}

// New constructs a Service. All dependencies are required — pass real
// implementations from fx (or test doubles from tests).
func New(trades tradelog.Log, snapshots perflog.SnapshotReader, stats StatsRunner) *Service {
	return &Service{trades: trades, snapshots: snapshots, statsDB: stats}
}

// ListTradesResult bundles trade rows with response Metadata.
type ListTradesResult struct {
	Trades   []tradelog.TradeRecord
	HasMore  bool
	Next     string
	Metadata domain.Metadata
}

// ListTradesParams is the input to ListTrades.
type ListTradesParams struct {
	Scope  domain.Scope
	Period domain.Period
	Limit  int
}

// ListTrades returns closed round-trip trades ordered exit_at DESC.
// Cursor pagination via Period.Before — pass the previous page's last exit_at.
func (s *Service) ListTrades(ctx context.Context, p ListTradesParams) (*ListTradesResult, error) {
	if err := validateScope(p.Scope); err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	period := p.Period.Resolve(time.Now().UTC())
	filter := tradelog.Filter{
		UserID: p.Scope.UserID,
		HelmID: p.Scope.HelmID,
		HandID: p.Scope.HandID,
		After:  period.After,
		Before: period.Before,
		Limit:  limit,
	}
	records, err := s.trades.Query(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list trades: %w", err)
	}
	res := &ListTradesResult{
		Trades:   records,
		HasMore:  len(records) == limit,
		Metadata: s.buildMetadata(ctx, p.Scope),
	}
	if res.HasMore && len(records) > 0 {
		res.Next = records[len(records)-1].ExitAt.UTC().Format(time.RFC3339)
	}
	return res, nil
}

// EquityCurveParams is the input to EquityCurve.
type EquityCurveParams struct {
	Scope      domain.Scope
	Period     domain.Period
	Resolution domain.Resolution
}

// EquityCurveResult bundles points with response Metadata.
type EquityCurveResult struct {
	Points   []domain.EquityPoint
	Metadata domain.Metadata
}

// EquityCurve returns a forward-filled equity curve at the requested resolution.
// Scope.HandID is honoured: nil → helm-level curve, set → that specific hand.
func (s *Service) EquityCurve(ctx context.Context, p EquityCurveParams) (*EquityCurveResult, error) {
	if err := validateScope(p.Scope); err != nil {
		return nil, err
	}
	if p.Scope.HelmID == nil {
		return nil, errors.New("equity curve requires a helm_id scope")
	}
	period := p.Period.Resolve(time.Now().UTC())
	points, err := s.snapshots.EquityCurve(ctx, *p.Scope.HelmID, p.Scope.HandID, period.After, period.Before, p.Resolution)
	if err != nil {
		return nil, fmt.Errorf("equity curve: %w", err)
	}
	return &EquityCurveResult{
		Points:   points,
		Metadata: s.buildMetadata(ctx, p.Scope),
	}, nil
}

// StatsParams is the input to ComputeStats.
type StatsParams struct {
	Scope  domain.Scope
	Period domain.Period
}

// StatsResult bundles computed KPIs with response Metadata.
type StatsResult struct {
	Stats    domain.Stats
	Metadata domain.Metadata
}

// ComputeStats runs the PG aggregation pipeline for the universal KPIs and
// returns the result. Attribution slices (by_symbol, by_pattern, by_exit) are
// included in the same response — single round-trip for the FE.
func (s *Service) ComputeStats(ctx context.Context, p StatsParams) (*StatsResult, error) {
	if err := validateScope(p.Scope); err != nil {
		return nil, err
	}
	period := p.Period.Resolve(time.Now().UTC())
	stats, err := s.statsDB.RunStats(ctx, p.Scope, period)
	if err != nil {
		return nil, fmt.Errorf("compute stats: %w", err)
	}
	return &StatsResult{Stats: stats, Metadata: s.buildMetadata(ctx, p.Scope)}, nil
}

// ListSnapshotsParams is the input to ListSnapshots.
type ListSnapshotsParams struct {
	Scope  domain.Scope
	Before time.Time // RFC3339 cursor exclusive; zero = newest
	Limit  int
}

// ListSnapshotsResult bundles raw snapshot rows with response Metadata.
type ListSnapshotsResult struct {
	Snapshots []perflog.SnapshotRow
	HasMore   bool
	Next      string
	Metadata  domain.Metadata
}

// ListSnapshots returns raw snapshot rows for audit / debug views.
// For chart consumers use EquityCurve instead (forward-filled, bucketed).
func (s *Service) ListSnapshots(ctx context.Context, p ListSnapshotsParams) (*ListSnapshotsResult, error) {
	if err := validateScope(p.Scope); err != nil {
		return nil, err
	}
	if p.Scope.HelmID == nil {
		return nil, errors.New("list snapshots requires a helm_id scope")
	}
	limit := p.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.snapshots.List(ctx, *p.Scope.HelmID, p.Scope.HandID, p.Before, limit)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	res := &ListSnapshotsResult{
		Snapshots: rows,
		HasMore:   len(rows) == limit,
		Metadata:  s.buildMetadata(ctx, p.Scope),
	}
	if res.HasMore && len(rows) > 0 {
		res.Next = rows[len(rows)-1].TS.UTC().Format(time.RFC3339)
	}
	return res, nil
}

// ── Internal helpers ────────────────────────────────────────────────────────

func validateScope(s domain.Scope) error {
	if s.UserID == uuid.Nil {
		return errors.New("user_id is required")
	}
	return nil
}

func (s *Service) buildMetadata(ctx context.Context, scope domain.Scope) domain.Metadata {
	md := domain.Metadata{
		Source:      "pg",
		GeneratedAt: time.Now().UTC(),
	}
	// Lag is per-helm; aggregate-across-helms queries report 0 (no single anchor).
	if scope.HelmID != nil {
		if lag, err := s.snapshots.PersisterLag(ctx, *scope.HelmID); err == nil {
			md.PersisterLagS = lag.Seconds()
		}
	}
	return md
}

// ── Decimal helpers exposed for the SQL stats runner ────────────────────────

// ZeroDecimal is the canonical zero used when SQL returns NULL aggregates.
var ZeroDecimal = decimal.Zero
