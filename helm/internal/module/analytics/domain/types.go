// Package domain defines the inputs/outputs of the analytics module.
// Pure types only — no infra dependencies.
package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Scope identifies what the analytics query is scoped to.
// Exactly one of HelmID / HandID must be set. UserID is always required for
// ownership enforcement at the service boundary.
type Scope struct {
	UserID uuid.UUID
	HelmID *uuid.UUID // nil = aggregate across user's helms
	HandID *uuid.UUID // nil = aggregate across helm's hands
}

// Period bounds a time-range query for stats/curves.
// At least one of After/Before must be set unless Preset is used.
type Period struct {
	After  time.Time
	Before time.Time
	Preset PeriodPreset // optional; overrides After/Before when non-zero
}

// PeriodPreset is a named rolling window. Resolved by the service into
// concrete After/Before timestamps based on time.Now().
type PeriodPreset string

const (
	PeriodAll PeriodPreset = ""
	Period24H PeriodPreset = "24h"
	Period7D  PeriodPreset = "7d"
	Period30D PeriodPreset = "30d"
	PeriodMTD PeriodPreset = "mtd"
	PeriodYTD PeriodPreset = "ytd"
)

// Resolve converts a Preset into concrete bounds anchored on now.
// Returns the receiver unchanged when Preset is empty.
func (p Period) Resolve(now time.Time) Period {
	if p.Preset == "" {
		return p
	}
	r := Period{Before: now}
	switch p.Preset {
	case Period24H:
		r.After = now.Add(-24 * time.Hour)
	case Period7D:
		r.After = now.AddDate(0, 0, -7)
	case Period30D:
		r.After = now.AddDate(0, 0, -30)
	case PeriodMTD:
		r.After = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	case PeriodYTD:
		r.After = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
	case PeriodAll:
		// Empty After == open lower bound.
	}
	return r
}

// Resolution is the bucket size for time-series outputs (equity curve).
type Resolution string

const (
	Res1m  Resolution = "1m"
	Res5m  Resolution = "5m"
	Res15m Resolution = "15m"
	Res1h  Resolution = "1h"
	Res4h  Resolution = "4h"
	Res1d  Resolution = "1d"
)

// SQL returns the Postgres interval expression for this resolution.
// Defaults to "1 minute" when Resolution is empty or unknown.
func (r Resolution) SQL() string {
	switch r {
	case Res5m:
		return "5 minutes"
	case Res15m:
		return "15 minutes"
	case Res1h:
		return "1 hour"
	case Res4h:
		return "4 hours"
	case Res1d:
		return "1 day"
	default:
		return "1 minute"
	}
}

// Metadata is attached to every analytics response so the FE can reason about
// freshness without polling Prometheus separately.
type Metadata struct {
	Source        string    `json:"source"`          // always "pg" today; left as field so callers don't pin assumption
	GeneratedAt   time.Time `json:"generated_at"`    // server-side time the response was built
	PersisterLagS float64   `json:"persister_lag_s"` // seconds since the last successful persister flush (≈ data freshness)
}

// Stats aggregates KPIs over a time window. All decimal-bearing fields use
// decimal.Decimal preserving full precision; ratios are float64 for ergonomic FE rendering.
type Stats struct {
	NTrades int     `json:"n_trades"`
	WinRate float64 `json:"win_rate"` // 0..1

	GrossProfit  decimal.Decimal `json:"gross_profit"`
	GrossLoss    decimal.Decimal `json:"gross_loss"` // positive value (Σ |losers|)
	NetPnL       decimal.Decimal `json:"net_pnl"`
	Commission   decimal.Decimal `json:"commission"`
	ProfitFactor float64         `json:"profit_factor"` // gross_profit / gross_loss; Inf when no losses

	AvgWin      decimal.Decimal `json:"avg_win"`
	AvgLoss     decimal.Decimal `json:"avg_loss"`     // negative
	Expectancy  decimal.Decimal `json:"expectancy"`   // $/trade
	ExpectancyR float64         `json:"expectancy_r"` // R/trade — avg r_multiple

	LargestWin  decimal.Decimal `json:"largest_win"`
	LargestLoss decimal.Decimal `json:"largest_loss"`

	AvgHoldingSeconds int `json:"avg_holding_seconds"`

	BySymbol  []GroupedKPI `json:"by_symbol,omitempty"`
	ByPattern []GroupedKPI `json:"by_pattern,omitempty"`
	ByExit    []GroupedKPI `json:"by_exit,omitempty"`
}

// GroupedKPI is a per-group slice used for attribution.
type GroupedKPI struct {
	Key     string          `json:"key"`
	NTrades int             `json:"n_trades"`
	WinRate float64         `json:"win_rate"`
	NetPnL  decimal.Decimal `json:"net_pnl"`
	AvgR    float64         `json:"avg_r"`
}

// EquityPoint is one bucket of the forward-filled equity curve.
type EquityPoint struct {
	TS            time.Time       `json:"ts"`
	Equity        decimal.Decimal `json:"equity"`
	Cash          decimal.Decimal `json:"cash"`
	RealizedPnL   decimal.Decimal `json:"realized_pnl"`
	UnrealizedPnL decimal.Decimal `json:"unrealized_pnl"`
}
