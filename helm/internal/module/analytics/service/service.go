// Package service implements the analytics read layer.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/tradelog"
	"mallow/helm/internal/module/analytics/domain"
)

// Service is the read-only analytics API. Handlers call here instead of
// directly hitting tradelog / snapshot readers so:
//   - request validation is centralised (scope, periods)
//   - response Metadata (lag, source) is uniform
//   - swapping the storage backend later requires no handler changes
type Service struct {
	trades  tradelog.Log
	statsDB StatsRunner
}

// StatsRunner exposes the raw SQL aggregations needed by ComputeStats.
// Separated so the service can be tested with a mocked PG layer.
type StatsRunner interface {
	RunStats(ctx context.Context, scope domain.Scope, p domain.Period) (domain.Stats, error)
}

// New constructs a Service. All dependencies are required — pass real
// implementations from fx (or test doubles from tests).
func New(trades tradelog.Log, stats StatsRunner) *Service {
	return &Service{trades: trades, statsDB: stats}
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
	filter := tradelog.TradeFilter{
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

// ── Internal helpers ────────────────────────────────────────────────────────

func validateScope(s domain.Scope) error {
	if s.UserID == uuid.Nil {
		return errors.New("user_id is required")
	}
	return nil
}

func (s *Service) buildMetadata(_ context.Context, _ domain.Scope) domain.Metadata {
	return domain.Metadata{
		Source:      "pg",
		GeneratedAt: time.Now().UTC(),
	}
}

// ── Decimal helpers exposed for the SQL stats runner ────────────────────────

// ZeroDecimal is the canonical zero used when SQL returns NULL aggregates.
var ZeroDecimal = decimal.Zero
