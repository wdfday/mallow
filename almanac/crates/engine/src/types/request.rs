//! Backtest input DTOs — consumed by the engine runner and HTTP handlers.

use serde::Deserialize;
use serde_json::Value;
use utoipa::ToSchema;

// ── BacktestRequest ───────────────────────────────────────────────────────────

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
    /// filtered out. Only meaningful for named strategies with variable strength.
    pub min_strength: Option<f64>,

    /// When present, run Monte Carlo bootstrap simulation after the backtest.
    pub monte_carlo: Option<MonteCarloConfig>,

    /// When present, run rolling walk-forward validation.
    pub walk_forward: Option<WalkForwardConfig>,

    /// Max number of points in equity/drawdown/rolling curves (default: 2000).
    /// Set to 0 to disable downsampling.
    pub curve_points: Option<usize>,

    /// Intra-bar stop detection mode.
    /// `"close_only"` (default) | `"pessimistic"` | `"ohlc_heuristic"`.
    pub intra_bar_mode: Option<String>,

    /// Candle type fed into strategy indicators.
    /// `"raw"` (default) | `"heiken_ashi"` | `"smooth_ha"`.
    /// Broker fills always use raw OHLCV prices regardless of this setting.
    pub candle_type: Option<String>,

    /// EMA smoothing period for `"smooth_ha"` (default: 3, minimum: 2).
    pub smooth_period: Option<usize>,
}

// ── RhaiBacktestRequest ───────────────────────────────────────────────────────

/// Dedicated request for Rhai-script backtests.
/// Takes a `script` field containing the full Rhai strategy.
///
/// Any `plot("name", value)` calls inside the script populate
/// `indicator_series` in the response.
///
/// Every request to this endpoint is persisted: strategy + case are created or
/// updated before the engine runs, and the result is saved on completion.
/// Use `case_id` to re-run an existing case (params may differ — the case row
/// is updated in place). Omit `case_id` to open a new case.
#[derive(Debug, Deserialize, ToSchema)]
pub struct RhaiBacktestRequest {
    /// Symbol to backtest, e.g. `"BTCUSDT"`.
    pub symbol: String,
    /// Full Rhai strategy script.
    pub script: String,

    // ── Persistence identity ─────────────────────────────────────────────────
    /// Strategy name (slug). Used to group versions; required for server-side runs.
    pub name: String,
    /// Human-readable case label. Defaults to `"{name} on {symbol}"` if omitted.
    pub label: Option<String>,
    /// ID of an existing strategy version to compare against.
    ///
    /// - Same script as that version → reuse it, open a new case.
    /// - Different script → create a new version with `previous_id = strategy_id`.
    ///
    /// When omitted, the server deduplicates by `spec_hash` across all versions
    /// of `name`; a new version is created only if no matching hash exists.
    pub strategy_id: Option<String>,
    /// Existing case ID to update and re-run. Omit to create a new case.
    pub case_id: Option<String>,
    /// Optional change notes attached to the strategy version.
    pub notes: Option<String>,

    // ── Date range ───────────────────────────────────────────────────────────
    pub from: Option<String>,
    pub to: Option<String>,

    // ── Sizing ───────────────────────────────────────────────────────────────
    pub initial_capital: Option<f64>,
    pub position_size_pct: Option<f64>,
    pub position_size_usd: Option<f64>,
    pub position_size_quantity: Option<f64>,
    pub max_positions: Option<usize>,

    // ── Execution ────────────────────────────────────────────────────────────
    pub commission_pct: Option<f64>,
    pub slippage_pct: Option<f64>,
    pub risk_free_annual: Option<f64>,
    pub intra_bar_mode: Option<String>,

    // ── Data ─────────────────────────────────────────────────────────────────
    pub data_source: Option<String>,
    pub asset_type: Option<String>,
    pub timeframe: Option<String>,

    // ── Engine options ───────────────────────────────────────────────────────
    pub candle_type: Option<String>,
    pub smooth_period: Option<usize>,
    pub curve_points: Option<usize>,
    pub monte_carlo: Option<MonteCarloConfig>,
    pub walk_forward: Option<WalkForwardConfig>,
}

impl From<RhaiBacktestRequest> for BacktestRequest {
    fn from(req: RhaiBacktestRequest) -> Self {
        BacktestRequest {
            strategy: "rhai".into(),
            symbol: req.symbol,
            params: Some(serde_json::json!({ "script": req.script })),
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
            data_source: req.data_source,
            asset_type: req.asset_type,
            timeframe: req.timeframe,
            min_strength: None,
            monte_carlo: req.monte_carlo,
            walk_forward: req.walk_forward,
            curve_points: req.curve_points,
            intra_bar_mode: req.intra_bar_mode,
            candle_type: req.candle_type,
            smooth_period: req.smooth_period,
        }
    }
}

// ── Sub-configs ───────────────────────────────────────────────────────────────

/// Walk-forward validation config.
#[derive(Debug, Deserialize, ToSchema)]
pub struct WalkForwardConfig {
    pub is_bars: usize,
    pub oos_bars: usize,
    pub step_bars: Option<usize>,
    /// `"rolling"` (default) | `"anchored"`.
    pub mode: Option<String>,
    pub embargo_bars: Option<usize>,
    pub min_oos_trades: Option<usize>,
}

/// Monte Carlo bootstrap config.
#[derive(Debug, Deserialize, ToSchema)]
pub struct MonteCarloConfig {
    /// Bootstrap iterations (default: 1 000).
    pub n_iter: Option<usize>,
    /// Ruin floor as fraction of initial capital (default: 0.50).
    pub ruin_threshold: Option<f64>,
    /// RNG seed for reproducibility (`null` = time-based).
    pub seed: Option<u64>,
}
