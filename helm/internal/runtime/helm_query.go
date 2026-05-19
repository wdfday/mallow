package runtime

import (
	"github.com/shopspring/decimal"

	"mallow/helm/internal/runtime/core/portfolio"
)

// Equity returns total account equity: cash + sum of open position market values.
func (r *HelmRuntime) Equity() decimal.Decimal {
	return r.Portfolio.Equity()
}

// Cash returns the available cash balance (equity minus deployed capital).
func (r *HelmRuntime) Cash() decimal.Decimal {
	return r.Portfolio.Cash()
}

// Positions returns a snapshot of all currently open positions.
func (r *HelmRuntime) Positions() []portfolio.Position {
	return r.Portfolio.Positions()
}

// Trades returns a snapshot of all completed trades recorded this session.
func (r *HelmRuntime) Trades() []portfolio.Trade {
	return r.Portfolio.Trades()
}

// EquityCurve returns the time-series equity log recorded since startup.
// Each point is a (timestamp, equity) pair snapshotted after every fill.
func (r *HelmRuntime) EquityCurve() []portfolio.EquityPoint {
	return r.Portfolio.EquityCurve()
}

// IsHalted reports whether the risk manager has tripped a circuit-breaker
// (daily loss limit or max drawdown). While halted, new entries are rejected.
func (r *HelmRuntime) IsHalted() bool {
	return r.RiskMgr.IsHalted()
}

// Price returns the last known market price for a symbol.
// Returns zero if no price has been received yet.
func (r *HelmRuntime) Price(symbol string) decimal.Decimal {
	return r.lastKnownPrice(symbol)
}
