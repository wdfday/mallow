//! Backtest output DTOs — produced by the engine runner, consumed by HTTP handlers.

use std::collections::HashMap;

use serde::Serialize;
use serde_json::Value;
use utoipa::ToSchema;

// ── Primitives ────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, ToSchema)]
pub struct CurvePoint {
    pub t: i64,
    pub v: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct ErrorResponse {
    pub error: String,
}

// ── Trade ─────────────────────────────────────────────────────────────────────

#[derive(Debug, Serialize, ToSchema)]
pub struct TradeResponse {
    pub symbol: String,
    pub side: String,
    pub qty: f64,
    pub entry_price: f64,
    pub exit_price: f64,
    pub entry_ts: i64,
    pub exit_ts: i64,
    pub entry_time: String,
    pub exit_time: String,
    pub pnl: f64,
    pub pnl_pct: f64,
    pub commission: f64,
    /// Max adverse excursion from entry (%).
    pub mae_pct: f64,
    /// Max favorable excursion from entry (%).
    pub mfe_pct: f64,
    pub bars_held: usize,
    pub exit_reason: String,
}

/// One order execution. Surfaced so clients can mark pyramiding *adds* (same-direction
/// fills into an open position) that the merged `trades` view collapses into one row.
#[derive(Debug, Serialize, ToSchema)]
pub struct FillResponse {
    pub t: i64,
    pub price: f64,
    pub qty: f64,
    /// `"buy"` or `"sell"`.
    pub side: String,
    /// Position key — base symbol, or `SYM#n` for an independent leg.
    pub sym: String,
    /// 0-based leg index (0 = base / first leg).
    pub leg: usize,
}

// ── Optional sections ─────────────────────────────────────────────────────────

#[derive(Debug, Serialize, ToSchema)]
pub struct RegimeTradeStatsResponse {
    pub label:          String,
    pub trades:         usize,
    pub win_rate_pct:   f64,
    pub avg_return_pct: f64,
    pub profit_factor:  f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct RegimeSummaryResponse {
    /// On-change transitions: (timestamp_ms, "trend/vol" label).
    pub changes: Vec<(i64, String)>,
    /// Trade performance broken down by regime label.
    pub trade_breakdown: Vec<RegimeTradeStatsResponse>,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct BuyHoldBenchmarkResponse {
    pub total_return_pct: f64,
    pub cagr_pct: f64,
    pub annualized_volatility_pct: f64,
    pub sharpe_ratio: f64,
    pub sortino_ratio: f64,
    pub max_drawdown_pct: f64,
    pub max_dd_duration_bars: usize,
}

/// Monte Carlo result — percentile equity bands + ruin probability.
#[derive(Debug, Serialize, ToSchema)]
pub struct MonteCarloResponse {
    pub n_iter: usize,
    pub n_trades: usize,
    pub initial_capital: f64,
    pub ruin_threshold: f64,
    pub ruin_probability: f64,
    pub final_p5: f64,
    pub final_p10: f64,
    pub final_p25: f64,
    pub final_p50: f64,
    pub final_p75: f64,
    pub final_p90: f64,
    pub final_p95: f64,
    pub curve_p5: Vec<f64>,
    pub curve_p10: Vec<f64>,
    pub curve_p25: Vec<f64>,
    pub curve_p50: Vec<f64>,
    pub curve_p75: Vec<f64>,
    pub curve_p90: Vec<f64>,
    pub curve_p95: Vec<f64>,
}

/// Per-window summary in a walk-forward result.
#[derive(Debug, Serialize, ToSchema)]
pub struct WalkForwardWindowResponse {
    pub window: usize,
    pub is_range: [usize; 2],
    pub oos_range: [usize; 2],
    pub is_sharpe: f64,
    pub is_return_pct: f64,
    pub is_trades: usize,
    pub oos_sharpe: f64,
    pub oos_sortino: f64,
    pub oos_calmar: f64,
    pub oos_return_pct: f64,
    pub oos_trades: usize,
    pub oos_win_rate_pct: f64,
    pub oos_max_drawdown_pct: f64,
    pub oos_profit_factor: f64,
    pub oos_expectancy: f64,
    pub oos_sqn: f64,
    pub oos_psr: f64,
    pub oos_equity_curve: Vec<CurvePoint>,
}

/// Walk-forward aggregate result.
#[derive(Debug, Serialize, ToSchema)]
pub struct WalkForwardResponse {
    pub windows: Vec<WalkForwardWindowResponse>,
    pub avg_oos_sharpe: f64,
    pub avg_oos_win_rate: f64,
    pub avg_oos_return_pct: f64,
    pub total_oos_trades: usize,
    pub pct_profitable_windows: f64,
    /// OOS Sharpe / IS Sharpe. > 0.7 robust, 0.5–0.7 marginal, < 0.5 overfit.
    pub efficiency_ratio: f64,
    pub efficiency_label: String,
    pub oos_equity_curve: Vec<CurvePoint>,
}

// ── Stats sub-structs ─────────────────────────────────────────────────────────

#[derive(Debug, Serialize, ToSchema)]
pub struct CapitalStats {
    pub initial: f64,
    pub final_equity: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct ReturnStats {
    pub total_pct: f64,
    pub cagr_pct: f64,
    pub annualized_volatility_pct: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct RiskAdjustedStats {
    pub sharpe: f64,
    pub sortino: f64,
    pub calmar: f64,
    pub serenity: f64,
    pub omega: f64,
    pub tail_ratio: f64,
    pub recovery_factor: f64,
    pub var_95: f64,
    pub cvar_95: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct DrawdownStats {
    pub max_pct: f64,
    pub max_duration_bars: usize,
    pub avg_pct: f64,
    pub ulcer_index: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct ExitReasonBreakdownResponse {
    pub signal: usize,
    pub stop_loss: usize,
    pub take_profit: usize,
    pub trailing_stop: usize,
    pub max_bars: usize,
    pub end_of_data: usize,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct TradeStats {
    pub total: usize,
    pub win_rate_pct: f64,
    pub profit_factor: f64,
    pub payoff_ratio: f64,
    pub expectancy: f64,
    pub breakeven_win_rate_pct: f64,
    pub gross_profit_usd: f64,
    pub gross_loss_usd: f64,
    pub avg_win_pct: f64,
    pub avg_loss_pct: f64,
    pub avg_duration_hours: f64,
    pub avg_bars_held_winners: f64,
    pub avg_bars_held_losers: f64,
    pub max_consecutive_losses: usize,
    pub max_consecutive_wins: usize,
    pub largest_win_pct: f64,
    pub largest_loss_pct: f64,
    pub mfe_capture_ratio: f64,
    pub exit_reasons: ExitReasonBreakdownResponse,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct DistributionStats {
    pub skewness: f64,
    pub excess_kurtosis: f64,
    pub sqn: f64,
    pub psr: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct LongShortStats {
    pub long_trades: usize,
    pub long_win_rate_pct: f64,
    pub long_profit_factor: f64,
    pub short_trades: usize,
    pub short_win_rate_pct: f64,
    pub short_profit_factor: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct ExcursionStats {
    pub avg_mae_pct: f64,
    pub avg_mfe_pct: f64,
    pub mae_mfe_ratio: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct ActivityStats {
    pub trades_per_year: f64,
    pub exposure_pct: f64,
    pub total_commission_usd: f64,
    pub kelly_pct: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct CurveStats {
    pub equity: Vec<CurvePoint>,
    pub drawdown: Vec<CurvePoint>,
    pub rolling_sharpe: Vec<f64>,
    pub rolling_sharpe_std: f64,
    pub rolling_drawdown: Vec<f64>,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct CalendarStats {
    /// Monthly returns: [year, month, return_pct]
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub monthly_returns: Vec<[f64; 3]>,
    /// Yearly returns: [year, annual_return_pct]
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub yearly_returns: Vec<[f64; 2]>,
}

// ── BacktestResponse ──────────────────────────────────────────────────────────

#[derive(Debug, Serialize, ToSchema)]
pub struct BacktestResponse {
    pub strategy: String,
    pub symbol: String,
    pub params: Value,
    pub timeframe: String,
    pub bar_count: usize,

    pub capital: CapitalStats,
    pub returns: ReturnStats,
    pub risk_adjusted: RiskAdjustedStats,
    pub drawdown: DrawdownStats,
    pub trade_stats: TradeStats,
    pub distribution: DistributionStats,
    pub long_short: LongShortStats,
    pub excursion: ExcursionStats,
    pub activity: ActivityStats,
    pub curves: CurveStats,
    pub calendar: CalendarStats,

    pub trades: Vec<TradeResponse>,

    /// Raw fills — only populated when pyramiding is active (`max_units > 1`), so
    /// normal single-entry backtests stay lean. Lets the chart mark each add.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub fills: Vec<FillResponse>,

    #[serde(skip_serializing_if = "HashMap::is_empty")]
    pub indicator_series: HashMap<String, Vec<CurvePoint>>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub regime_summary: Option<RegimeSummaryResponse>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub benchmark: Option<BuyHoldBenchmarkResponse>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub monte_carlo: Option<MonteCarloResponse>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub walk_forward: Option<WalkForwardResponse>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub walk_forward_note: Option<String>,
}
