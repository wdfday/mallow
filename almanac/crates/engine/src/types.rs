//! Backtest request / response DTOs.
//!
//! Moved from the former `logbook` crate when the HTTP surface was
//! consolidated into `herald`. These types describe a *backtest run* — an
//! engine-level concept — so they live alongside the engine that consumes
//! them. Strategy-metadata lives in [`alm_strategy::catalog`].
//!
//! `ToSchema` / `IntoParams` derives are kept so HTTP layers can opt into
//! Swagger without redefining the schema.

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;
use utoipa::ToSchema;

// ── Request ──────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, ToSchema)]
pub struct BacktestRequest {
    /// Strategy key, e.g. `"rsi_mean_rev"`. See `alm-strategy::catalog::STRATEGY_KEYS`.
    pub strategy: String,

    /// Asset symbol, e.g. `"BTCUSDT"`.
    pub symbol: String,

    /// Strategy-specific parameters as a JSON object.
    ///
    /// All values are scalars (`f64` or `u64`), except for array params such as
    /// GMMA period lists. Unknown keys are silently ignored; missing keys use
    /// per-strategy defaults.
    pub params: Option<Value>,

    /// Exit / risk-management overrides. When `None`, strategy signal-based
    /// exits are used.
    pub exit: Option<ExitConfig>,

    /// Date range start, inclusive. Format: `"YYYY-MM-DD"`.
    pub from: Option<String>,

    /// Date range end, inclusive. Format: `"YYYY-MM-DD"`.
    pub to: Option<String>,

    /// Starting capital in USD (default: 10 000).
    pub initial_capital: Option<f64>,

    /// Commission per trade as a fraction of trade value (default: 0.001).
    pub commission_pct: Option<f64>,

    /// Slippage per trade as a fraction of trade value (default: 0.0005).
    pub slippage_pct: Option<f64>,

    /// Risk-free annual rate used for Sharpe/Sortino (default: 0.04).
    pub risk_free_annual: Option<f64>,

    /// Fraction of equity to allocate per trade (default: 0.95).
    pub position_size_pct: Option<f64>,

    /// Fixed dollar amount per trade. Overrides `position_size_pct`.
    pub position_size_usd: Option<f64>,

    /// Fixed quantity per trade. Overrides both USD and pct sizing.
    pub position_size_quantity: Option<f64>,

    /// Maximum simultaneous open positions (default: 1).
    pub max_positions: Option<usize>,

    /// Drop bars outside regular market hours.
    pub market_hours_only: Option<bool>,

    /// Data source — selects which provider subdirectory to load bars from.
    /// Values: `"polygon"` (US stocks), `"bnb"` (Binance crypto),
    /// `"vci"` (VN stocks), `"okx"` (OKX crypto).
    /// Derives market region automatically: polygon→us, vci→vn, bnb/okx→crypto (no filter).
    pub data_source: Option<String>,

    /// Asset class — controls lot sizing.
    /// `"crypto"` → fractional qty; `"stock"` → whole shares; `"vn_stock"` → lots of 100.
    pub asset_type: Option<String>,

    /// Timeframe subdirectory, e.g. `"H1"`, `"M1"`, `"D1"`.
    pub timeframe: Option<String>,

    /// Minimum signal strength [0.0, 1.0] — signals below this threshold are
    /// filtered out. Only meaningful for named strategies with variable strength
    /// (CEL/Dynamic always emit 1.0).
    pub min_strength: Option<f64>,

    /// When present, run Monte Carlo bootstrap simulation after the backtest
    /// and include the result in the response.
    pub monte_carlo: Option<MonteCarloConfig>,

    /// When present, run rolling walk-forward validation and include the result.
    pub walk_forward: Option<WalkForwardConfig>,
}

// ── Exit config ──────────────────────────────────────────────────────────────

/// An exit level expressed as either a fixed fraction **or** an ATR multiple.
///
/// JSON examples:
/// - `0.05`         → fixed 5% from entry
/// - `"1.5*atr"`    → 1.5 × ATR(14)
/// - `"2*atr(21)"`  → 2 × ATR(21)
#[derive(Debug, Clone, Deserialize, Serialize, ToSchema)]
#[serde(untagged)]
pub enum ExitLevel {
    Pct(f64),
    Expr(String),
}

impl ExitLevel {
    /// Parse an ATR-multiple expression → `(multiplier, period)`.
    pub fn as_atr(&self) -> Option<(f64, usize)> {
        let s = match self {
            Self::Expr(s) => s.trim(),
            Self::Pct(_) => return None,
        };
        let s_low = s.to_lowercase();
        let base = s_low.strip_suffix(')').and_then(|b| {
            let idx = b.find("*atr(")?;
            Some((&b[..idx], &b[idx + 5..]))
        });

        if let Some((mult_str, period_str)) = base {
            let mult = mult_str.trim().parse::<f64>().ok()?;
            let period = period_str.trim().parse::<usize>().ok()?;
            return Some((mult, period));
        }

        if let Some(mult_str) = s_low.strip_suffix("*atr") {
            let mult = mult_str.trim().parse::<f64>().ok()?;
            return Some((mult, 14));
        }

        None
    }

    /// Fixed-pct value, or `None` when this is an ATR expression.
    pub fn as_pct(&self) -> Option<f64> {
        match self {
            Self::Pct(v) => Some(*v),
            Self::Expr(_) => None,
        }
    }
}

/// Exit / risk-management overrides applied on top of strategy signal logic.
#[derive(Debug, Clone, Deserialize, Default, ToSchema, Serialize)]
pub struct ExitConfig {
    /// Take-profit. Fixed `0.10` or ATR expression `"3*atr(14)"`.
    pub tp: Option<ExitLevel>,
    /// Stop-loss. Fixed `0.05` or ATR expression `"1.5*atr"`.
    pub sl: Option<ExitLevel>,
    /// Trailing stop as a fraction from the running peak. Fixed only (e.g. `0.05` = 5%).
    pub trailing_stop: Option<f64>,
    /// Time-based exit after this many bars in position.
    pub max_bars: Option<usize>,
    /// Intra-bar stop detection mode.
    /// `"close_only"` (default) — check only at bar close.
    /// `"pessimistic"` — check stop vs bar.low (long) / bar.high (short); fill at stop level.
    /// `"ohlc_heuristic"` — same as pessimistic but uses bar direction to break SL/TP ties.
    pub intra_bar_mode: Option<String>,
}

// ── Monte Carlo ───────────────────────────────────────────────────────────────

/// Walk-forward validation config — attach to a backtest request to get
/// rolling IS/OOS consistency analysis alongside the normal result.
#[derive(Debug, Deserialize, ToSchema)]
pub struct WalkForwardConfig {
    /// Number of bars in each in-sample training window.
    pub is_bars: usize,
    /// Minimum bars per out-of-sample test window.
    pub oos_bars: usize,
    /// Bars to advance per step. Defaults to `oos_bars` (non-overlapping OOS).
    pub step_bars: Option<usize>,
    /// `"rolling"` (default) — IS window slides forward each step.
    /// `"anchored"` — IS always starts at bar 0, grows each step.
    pub mode: Option<String>,
    /// Bars to skip at the start of each OOS window for indicator warm-up.
    pub embargo_bars: Option<usize>,
    /// Extend OOS window (in `oos_bars` increments) until at least this many trades occur.
    pub min_oos_trades: Option<usize>,
}

/// Per-window summary in a walk-forward result.
#[derive(Debug, Serialize, ToSchema)]
pub struct WalkForwardWindowResponse {
    pub window: usize,
    pub is_range: [usize; 2],
    pub oos_range: [usize; 2],
    // IS
    pub is_sharpe: f64,
    pub is_return_pct: f64,
    pub is_trades: usize,
    // OOS
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

/// Walk-forward result — aggregate + per-window breakdown.
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
    /// Human-readable label: `"robust"` / `"marginal"` / `"overfit"` / `"no_data"`.
    pub efficiency_label: String,
    /// All OOS windows stitched into one continuous equity curve.
    pub oos_equity_curve: Vec<CurvePoint>,
}

/// Monte Carlo simulation config — attach to a backtest request to get
/// bootstrap robustness bands alongside the normal result.
#[derive(Debug, Deserialize, ToSchema)]
pub struct MonteCarloConfig {
    /// Bootstrap iterations (default 1 000).
    pub n_iter: Option<usize>,
    /// Equity fraction below which a simulation counts as ruin (default 0.50).
    pub ruin_threshold: Option<f64>,
    /// RNG seed for reproducibility (`null` = time-based).
    pub seed: Option<u64>,
}

/// Monte Carlo result — percentile equity bands + ruin probability.
#[derive(Debug, Serialize, ToSchema)]
pub struct MonteCarloResponse {
    pub n_iter: usize,
    pub n_trades: usize,
    pub initial_capital: f64,
    pub ruin_threshold: f64,
    /// Fraction of simulations that crossed the ruin floor.
    pub ruin_probability: f64,
    // ── Final-equity percentiles ─────────────────────────────────────────
    pub final_p5: f64,
    pub final_p10: f64,
    pub final_p25: f64,
    pub final_p50: f64,
    pub final_p75: f64,
    pub final_p90: f64,
    pub final_p95: f64,
    // ── Equity-curve percentile bands (length = n_trades + 1) ────────────
    pub curve_p5: Vec<f64>,
    pub curve_p10: Vec<f64>,
    pub curve_p25: Vec<f64>,
    pub curve_p50: Vec<f64>,
    pub curve_p75: Vec<f64>,
    pub curve_p90: Vec<f64>,
    pub curve_p95: Vec<f64>,
}

// ── Response ─────────────────────────────────────────────────────────────────

#[derive(Debug, Serialize, ToSchema)]
pub struct CurvePoint {
    pub t: i64,
    pub v: f64,
}

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

#[derive(Debug, Serialize, ToSchema)]
pub struct RegimeSummaryResponse {
    pub trending_pct: f64,
    pub ranging_pct: f64,
    pub neutral_pct: f64,
    pub high_vol_pct: f64,
    pub low_vol_pct: f64,
    pub changes: Vec<(i64, String)>,
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

// ── Grouped response sub-structs ──────────────────────────────────────────────

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
    pub atr_stop: usize,
    pub atr_target: usize,
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

#[derive(Debug, Serialize, ToSchema)]
pub struct BacktestResponse {
    pub strategy: String,
    pub symbol: String,
    pub params: Value,
    pub timeframe: String,

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

#[derive(Debug, Serialize, ToSchema)]
pub struct ErrorResponse {
    pub error: String,
}

// ── CEL backtest request ─────────────────────────────────────────────────────

#[derive(Debug, Deserialize, ToSchema)]
pub struct CelBacktestRequest {
    pub symbol: String,
    pub entry_expr: String,
    pub exit_expr: String,
    /// Named expression aliases expanded into `entry_expr`/`exit_expr` before compilation.
    ///
    /// Example: `{"trend": "adx(14) > 25.0", "oversold": "rsi(14) < 30.0"}`
    /// then `entry_expr: "trend && oversold"` expands to `"(adx(14) > 25.0) && (rsi(14) < 30.0)"`.
    ///
    /// Defines may reference other defines (resolved in dependency order).
    /// Circular references and shadowing of built-in names are rejected.
    pub defines: Option<HashMap<String, String>>,
    pub exit: Option<ExitConfig>,
    pub candle_type: Option<String>,
    pub ha_smooth: Option<usize>,
    pub from: Option<String>,
    pub to: Option<String>,
    pub initial_capital: Option<f64>,
    pub commission_pct: Option<f64>,
    pub slippage_pct: Option<f64>,
    pub risk_free_annual: Option<f64>,
    pub position_size_pct: Option<f64>,
    pub position_size_usd: Option<f64>,
    pub position_size_quantity: Option<f64>,
    pub max_positions: Option<usize>,
    pub market_hours_only: Option<bool>,
    pub data_source: Option<String>,
    pub asset_type: Option<String>,
    pub timeframe: Option<String>,
    pub min_strength: Option<f64>,
    pub monte_carlo: Option<MonteCarloConfig>,
    pub walk_forward: Option<WalkForwardConfig>,
}

// ── RhaiBacktestRequest ──────────────────────────────────────────────────────

/// Dedicated request for Rhai-script backtests — mirrors `CelBacktestRequest`
/// but takes a single `script` field instead of `entry_expr` / `exit_expr`.
///
/// The script is passed verbatim to `RhaiStrategy`; any `plot("name", value)`
/// calls inside the script populate `indicator_series` in the response.
#[derive(Debug, Deserialize, ToSchema)]
pub struct RhaiBacktestRequest {
    pub symbol: String,
    /// Full Rhai script. Declare indicators with `indicator("type", period)`,
    /// return entry signal from `entry` variable, exit from `exit` variable.
    pub script: String,
    pub exit: Option<ExitConfig>,
    pub from: Option<String>,
    pub to: Option<String>,
    pub initial_capital: Option<f64>,
    pub commission_pct: Option<f64>,
    pub slippage_pct: Option<f64>,
    pub risk_free_annual: Option<f64>,
    pub position_size_pct: Option<f64>,
    pub position_size_usd: Option<f64>,
    pub position_size_quantity: Option<f64>,
    pub max_positions: Option<usize>,
    pub market_hours_only: Option<bool>,
    pub data_source: Option<String>,
    pub asset_type: Option<String>,
    pub timeframe: Option<String>,
    pub monte_carlo: Option<MonteCarloConfig>,
    pub walk_forward: Option<WalkForwardConfig>,
}

impl From<RhaiBacktestRequest> for BacktestRequest {
    fn from(req: RhaiBacktestRequest) -> Self {
        BacktestRequest {
            strategy: "rhai".into(),
            symbol: req.symbol,
            params: Some(serde_json::json!({ "script": req.script })),
            exit: req.exit,
            from: req.from,
            to: req.to,
            initial_capital: req.initial_capital,
            commission_pct: req.commission_pct,
            slippage_pct: req.slippage_pct,
            risk_free_annual: req.risk_free_annual,
            position_size_pct: req.position_size_pct,
            position_size_usd: req.position_size_usd,
            position_size_quantity: req.position_size_quantity,
            max_positions: req.max_positions,
            market_hours_only: req.market_hours_only,
            data_source: req.data_source,
            asset_type: req.asset_type,
            timeframe: req.timeframe,
            min_strength: None,
            monte_carlo: req.monte_carlo,
            walk_forward: req.walk_forward,
        }
    }
}

// ── DynamicBacktestRequest — DEPRECATED (use CelBacktestRequest) ────────────
/*
#[derive(Debug, Deserialize, ToSchema)]
pub struct DynamicBacktestRequest {
    pub symbol: String,
    pub indicators: serde_json::Map<String, serde_json::Value>,
    pub entry: serde_json::Value,
    pub exit: serde_json::Value,
    pub exit_rules: Option<ExitConfig>,
    pub from: Option<String>,
    pub to: Option<String>,
    pub initial_capital: Option<f64>,
    pub commission_pct: Option<f64>,
    pub slippage_pct: Option<f64>,
    pub risk_free_annual: Option<f64>,
    pub position_size_pct: Option<f64>,
    pub position_size_usd: Option<f64>,
    pub position_size_quantity: Option<f64>,
    pub max_positions: Option<usize>,
    pub market_hours_only: Option<bool>,
    pub data_source: Option<String>,
    pub asset_type: Option<String>,
    pub timeframe: Option<String>,
    pub min_strength: Option<f64>,
    pub monte_carlo: Option<MonteCarloConfig>,
    pub walk_forward: Option<WalkForwardConfig>,
}
*/

// ── Request conversions ──────────────────────────────────────────────────────
//
// CEL → canonical BacktestRequest. Lives here (not in `backtest.rs`)
// so the conversion is a pure type transformation with no engine imports —
// both the HTTP handler and library users can apply it without pulling the
// whole runner into scope.

impl From<CelBacktestRequest> for BacktestRequest {
    fn from(req: CelBacktestRequest) -> Self {
        let mut params = serde_json::Map::new();
        params.insert("entry".into(), serde_json::json!(req.entry_expr));
        params.insert("exit".into(), serde_json::json!(req.exit_expr));
        if let Some(defs) = req.defines {
            let obj: serde_json::Map<_, _> = defs
                .into_iter()
                .map(|(k, v)| (k, serde_json::json!(v)))
                .collect();
            params.insert("defines".into(), serde_json::Value::Object(obj));
        }
        if let Some(v) = req.candle_type {
            params.insert("candle_type".into(), serde_json::json!(v));
        }
        if let Some(v) = req.ha_smooth {
            params.insert("ha_smooth".into(), serde_json::json!(v));
        }
        if let Some(ref cfg) = req.exit {
            crate::backtest::inject_atr_exit_into_cel_params(cfg, &mut params);
        }
        BacktestRequest {
            strategy: "cel".into(),
            symbol: req.symbol,
            params: Some(Value::Object(params)),
            exit: req.exit,
            from: req.from,
            to: req.to,
            initial_capital: req.initial_capital,
            commission_pct: req.commission_pct,
            slippage_pct: req.slippage_pct,
            risk_free_annual: req.risk_free_annual,
            position_size_pct: req.position_size_pct,
            position_size_usd: req.position_size_usd,
            position_size_quantity: req.position_size_quantity,
            max_positions: req.max_positions,
            market_hours_only: req.market_hours_only,
            data_source: req.data_source,
            asset_type: req.asset_type,
            timeframe: req.timeframe,
            min_strength: req.min_strength,
            monte_carlo: req.monte_carlo,
            walk_forward: req.walk_forward,
        }
    }
}

// impl From<DynamicBacktestRequest> for BacktestRequest — DEPRECATED
/*
impl From<DynamicBacktestRequest> for BacktestRequest {
    fn from(req: DynamicBacktestRequest) -> Self {
        let mut params = serde_json::Map::new();
        params.insert("indicators".into(), Value::Object(req.indicators));
        params.insert("entry".into(), req.entry);
        params.insert("exit".into(), req.exit);
        BacktestRequest {
            strategy: "dynamic".into(),
            symbol: req.symbol,
            params: Some(Value::Object(params)),
            exit: req.exit_rules,
            from: req.from,
            to: req.to,
            initial_capital: req.initial_capital,
            commission_pct: req.commission_pct,
            slippage_pct: req.slippage_pct,
            risk_free_annual: req.risk_free_annual,
            position_size_pct: req.position_size_pct,
            position_size_usd: req.position_size_usd,
            position_size_quantity: req.position_size_quantity,
            max_positions: req.max_positions,
            market_hours_only: req.market_hours_only,
            data_source: req.data_source,
            asset_type: req.asset_type,
            timeframe: req.timeframe,
            min_strength: req.min_strength,
            monte_carlo: req.monte_carlo,
            walk_forward: req.walk_forward,
        }
    }
}
*/
