// Package portfolio tracks the in-memory financial state of one broker account:
// cash balance, open positions, completed round-trip trades, and an equity curve.
//
// # Responsibilities
//
// Portfolio answers three categories of questions:
//   - Balances:   Cash(), Equity() — what do I have right now?
//   - Positions:  Positions(), GetPosition(symbol) — what am I holding?
//   - History:    Trades(), EquityCurve() — what has happened?
//
// It also exposes derived analytics (TotalReturn, CurrentDrawdown, MaxDrawdown,
// WinRate, DailyPnL) and a one-call Summary snapshot for API responses.
//
// # What Portfolio Does NOT Do
//
// Portfolio is purely a bookkeeping layer. It does not:
//   - make trading decisions (that is the strategy + tactics layers),
//   - enforce risk limits (that is the risk.Manager),
//   - place or track orders (that is the exchange adapter + HelmRuntime),
//   - persist state to disk (the poslog reconciler owns crash recovery).
//
// # Update Paths
//
// There are two ways state enters the portfolio:
//
//  1. Event-driven (normal path): ApplyFill is called by hand_handling.go after
//     every confirmed order fill. It updates cash, recalculates avg entry price,
//     and records a completed Trade when a position is fully closed.
//
//  2. Authoritative sync (periodic path): ApplySync is called by the helm sync
//     cycle (every SYNC_INTERVAL, default 5 min) with live data from the exchange
//     REST API. It wholesale-replaces cash and positions with the ground truth,
//     correcting any drift from missed fills or partial fills.
//     ApplySync does NOT touch equityCurve or trades — those remain append-only.
//
// On startup, RestorePosition re-injects positions from the poslog replay without
// touching cash, allowing the reconciler to rebuild in-memory state from the
// durable event log before the first sync cycle runs.
//
// # Thread Safety
//
// All exported methods are safe for concurrent use. A single sync.RWMutex
// serialises writes; reads hold the read lock. Internal helpers suffixed
// "Locked" assume the caller already holds the write lock and must not
// be called from outside the package.
//
// # Position Sign Convention
//
// Qty is positive for long positions, negative for short positions.
// ApplyFill handles sign arithmetic automatically based on fill.Side.
// RestorePosition accepts an explicit side string ("buy"/"sell") and negates
// qty internally.
//
// # Equity Calculation
//
// Equity = cash + Σ(position.Qty × position.CurrentPrice)
//
// CurrentPrice is updated in real-time by UpdatePrice (called on every price tick
// delivered to HelmRuntime). The equity curve is only appended to explicitly
// via RecordEquity — it is not auto-updated on every tick to avoid unbounded growth.
package portfolio
