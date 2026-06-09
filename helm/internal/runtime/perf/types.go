// Package perf computes live performance reports for running hands.
// A HandReport is a superset of the backtest BacktestReport: all statistical
// metrics (Sharpe, Sortino, Calmar, drawdown, win-rate …) plus execution-quality
// metrics that only exist in live trading (slippage, fill latency, rejection rate).
package perf

import (
	"time"

	"github.com/shopspring/decimal"
)

// ── Statistical performance ───────────────────────────────────────────────────

// ReturnStats holds standard risk-adjusted return metrics.
// All ratios use daily log-returns; annualized with 365/252 trading days.
type ReturnStats struct {
	TotalReturnPct   float64 `json:"total_return_pct"`
	CAGR             float64 `json:"cagr_pct"`     // annualized growth rate; 0 if uptime < 1 day
	SharpeRatio      float64 `json:"sharpe_ratio"` // 0 if < 2 data points
	SortinoRatio     float64 `json:"sortino_ratio"`
	CalmarRatio      float64 `json:"calmar_ratio"` // CAGR / MaxDrawdownPct; 0 if no drawdown
	MaxDrawdownPct   float64 `json:"max_drawdown_pct"`
	CurrentDDPct     float64 `json:"current_drawdown_pct"`
	VolatilityAnnPct float64 `json:"volatility_ann_pct"` // annualized daily-return std-dev
}

// TradeStats summarises closed round-trip trades.
type TradeStats struct {
	TotalTrades      int             `json:"total_trades"`
	WinCount         int             `json:"win_count"`
	LossCount        int             `json:"loss_count"`
	WinRatePct       float64         `json:"win_rate_pct"`
	ProfitFactor     float64         `json:"profit_factor"` // gross profit / gross loss; +Inf if no losses
	AvgWin           decimal.Decimal `json:"avg_win"`
	AvgLoss          decimal.Decimal `json:"avg_loss"`   // absolute value
	AvgRR            float64         `json:"avg_rr"`     // avg win / avg loss
	Expectancy       decimal.Decimal `json:"expectancy"` // (winRate * avgWin) - (lossRate * avgLoss)
	LargestWin       decimal.Decimal `json:"largest_win"`
	LargestLoss      decimal.Decimal `json:"largest_loss"` // absolute value
	AvgHoldDuration  time.Duration   `json:"avg_hold_duration_ns"`
	MaxHoldDuration  time.Duration   `json:"max_hold_duration_ns"`
	TotalRealizedPnL decimal.Decimal `json:"total_realized_pnl"`
}

// ── Execution quality (live-only) ─────────────────────────────────────────────

// ExecutionStats captures live trading execution quality.
// These metrics have no backtest equivalent (simulated fills are idealised).
type ExecutionStats struct {
	// Slippage: difference between signal/mid price and actual fill price.
	AvgSlippagePct    float64         `json:"avg_slippage_pct"`
	MaxSlippagePct    float64         `json:"max_slippage_pct"`
	TotalSlippageCost decimal.Decimal `json:"total_slippage_cost"`

	// Fill latency: time from order placement to fill confirmation.
	AvgFillLatencyMs float64 `json:"avg_fill_latency_ms"`
	MaxFillLatencyMs float64 `json:"max_fill_latency_ms"`

	// Signal-to-order latency: time from signal receipt to order placement.
	AvgSignalLatencyMs float64 `json:"avg_signal_latency_ms"`

	// Fill quality.
	TotalOrders    int64   `json:"total_orders"`
	FilledOrders   int64   `json:"filled_orders"`
	RejectedOrders int64   `json:"rejected_orders"`
	PartialFills   int64   `json:"partial_fills"`
	FillRatePct    float64 `json:"fill_rate_pct"` // filled / total

	// Commission.
	TotalCommission decimal.Decimal `json:"total_commission"`
}

// ── Signal activity ───────────────────────────────────────────────────────────

// SignalStats tracks signal pipeline throughput.
type SignalStats struct {
	Received int64 `json:"received"`
	Filtered int64 `json:"filtered"` // blocked by risk/regime filter
	Dropped  int64 `json:"dropped"`  // non-urgent signals lost (channel full)
	Acted    int64 `json:"acted"`    // signals that resulted in an order
}

// ── Top-level report ─────────────────────────────────────────────────────────

// HandReport is the full live performance report for a single hand.
// It is a superset of the backtest BacktestReport.
type HandReport struct {
	HandID   string    `json:"hand_id"`
	Symbol   string    `json:"symbol"`
	Strategy string    `json:"strategy"`
	Since    time.Time `json:"since"` // when the hand last started
	AsOf     time.Time `json:"as_of"` // report generation time
	Uptime   string    `json:"uptime"`

	Returns   ReturnStats    `json:"returns"`
	Trades    TradeStats     `json:"trades"`
	Execution ExecutionStats `json:"execution"`
	Signals   SignalStats    `json:"signals"`

	// Current open exposure.
	OpenPositions int             `json:"open_positions"`
	UnrealizedPnL decimal.Decimal `json:"unrealized_pnl"`
	CurrentEquity decimal.Decimal `json:"current_equity"`
}
