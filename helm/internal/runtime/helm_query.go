package runtime

import (
	"github.com/shopspring/decimal"

	"mallow/helm/internal/infra/exchange"
	"mallow/helm/internal/runtime/core/portfolio"
)

// Equity returns total account equity: cash + sum of open position market values.
func (r *HelmRuntime) Equity() decimal.Decimal {
	return r.Portfolio.Equity()
}

// Cash returns the total exchange cash balance.
func (r *HelmRuntime) Cash() decimal.Decimal {
	return r.Portfolio.Cash()
}

// AvailableCash computes the cash available for manual trading:
//
//	totalCash − Σ(allocated + cumPnL − deployed)  for each hand
//
// 's "logical cash" is its remaining undeployed budget. Summing those
// and subtracting from total cash gives the portion not spoken-for by any hand.
// All hands are required to have a positive AllocatedCapital.
func (r *HelmRuntime) AvailableCash() decimal.Decimal {
	r.mu.RLock()
	defer r.mu.RUnlock()

	totalCash := r.Portfolio.Cash()
	var handCashSum decimal.Decimal

	for _, hand := range r.hands {
		metrics := hand.Metrics()
		deployed := hand.DeployedCapital()
		handCash := hand.AllocatedCapital().Add(metrics.TotalPnL).Sub(deployed)
		if handCash.IsPositive() {
			handCashSum = handCashSum.Add(handCash)
		}
	}

	available := totalCash.Sub(handCashSum)
	if available.IsNegative() {
		return decimal.Zero
	}
	return available
}

// PortfolioSummary returns the portfolio summary with available cash correctly adjusted.
// OpenPositions is overridden with the true unit count (sum of active legs across all hands)
// rather than the portfolio's symbol-deduped position map length.
func (r *HelmRuntime) PortfolioSummary() portfolio.Summary {
	s := r.Portfolio.Summary()
	s.AvailableCash = r.AvailableCash()
	s.OpenPositions = r.OpenUnitCount()
	return s
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
// Each point is a (timestamp, equity) pair snapshot after every fill.
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

// ExchangeSnapshot returns a point-in-time copy of exchange latency/error metrics.
// Returns nil if the underlying exchange adapter is not instrumented with MeteredExchange.
func (r *HelmRuntime) ExchangeSnapshot() *exchange.MetricsSnapshot {
	if m, ok := r.Exchange.(*exchange.MeteredExchange); ok {
		return new(m.Snapshot())
	}
	return nil
}
