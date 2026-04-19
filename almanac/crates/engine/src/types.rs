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

    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,

    /// Asset class — controls lot sizing.
    /// `"crypto"` → fractional qty; `"stock"` → whole shares; `"vn_stock"` → lots of 100.
    pub asset_type: Option<String>,

    /// Timeframe subdirectory, e.g. `"H1"`, `"M1"`, `"D1"`.
    pub timeframe: Option<String>,
}

// ── Exit config ──────────────────────────────────────────────────────────────

/// An exit level expressed as either a fixed fraction **or** an ATR multiple.
///
/// JSON examples:
/// - `0.05`         → fixed 5% from entry
/// - `"1.5*atr"`    → 1.5 × ATR(14)
/// - `"2*atr(21)"`  → 2 × ATR(21)
#[derive(Debug, Clone, Deserialize, ToSchema)]
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
#[derive(Debug, Clone, Deserialize, Default, ToSchema)]
pub struct ExitConfig {
    /// Take-profit. Fixed `0.10` or ATR expression `"3*atr(14)"`.
    pub tp: Option<ExitLevel>,
    /// Stop-loss. Fixed `0.05` or ATR expression `"1.5*atr"`.
    pub sl: Option<ExitLevel>,
    /// Time-based exit after this many bars in position.
    pub max_bars: Option<usize>,
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

#[derive(Debug, Serialize, ToSchema)]
pub struct BacktestResponse {
    pub strategy: String,
    pub symbol: String,
    pub params: Value,

    // Capital
    pub initial_capital: f64,
    pub final_equity: f64,

    // Return metrics
    pub total_return: f64,
    pub cagr: f64,
    pub annualized_volatility: f64,

    // Risk-adjusted
    pub sharpe_ratio: f64,
    pub sortino_ratio: f64,
    pub calmar_ratio: f64,

    // Drawdown
    pub max_drawdown: f64,
    pub max_dd_duration_bars: usize,
    pub avg_drawdown: f64,

    // Trade stats
    pub total_trades: usize,
    pub win_rate: f64,
    pub profit_factor: f64,
    pub expectancy: f64,
    pub avg_win: f64,
    pub avg_loss: f64,
    pub avg_trade_duration_hours: f64,
    pub max_consecutive_losses: usize,

    pub trades: Vec<TradeResponse>,
    pub equity_curve: Vec<CurvePoint>,
    pub drawdown_curve: Vec<CurvePoint>,

    #[serde(skip_serializing_if = "HashMap::is_empty")]
    pub indicator_series: HashMap<String, Vec<CurvePoint>>,

    // Advanced risk metrics
    pub var_95: f64,
    pub cvar_95: f64,
    pub omega_ratio: f64,
    pub tail_ratio: f64,
    pub recovery_factor: f64,

    pub rolling_sharpe: Vec<f64>,
    pub rolling_drawdown: Vec<f64>,

    pub timeframe: String,
    pub exposure_pct: f64,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub regime_summary: Option<RegimeSummaryResponse>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub benchmark: Option<BuyHoldBenchmarkResponse>,
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
    pub exchange: Option<String>,
    pub asset_type: Option<String>,
    pub timeframe: Option<String>,
}

// ── Dynamic backtest request ─────────────────────────────────────────────────

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
    pub exchange: Option<String>,
    pub asset_type: Option<String>,
    pub timeframe: Option<String>,
}

// ── Request conversions ──────────────────────────────────────────────────────
//
// CEL / Dynamic → canonical BacktestRequest. Lives here (not in `backtest.rs`)
// so the conversion is a pure type transformation with no engine imports —
// both the HTTP handler and library users can apply it without pulling the
// whole runner into scope.

impl From<CelBacktestRequest> for BacktestRequest {
    fn from(req: CelBacktestRequest) -> Self {
        let mut params = serde_json::Map::new();
        params.insert("entry".into(), serde_json::json!(req.entry_expr));
        params.insert("exit".into(), serde_json::json!(req.exit_expr));
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
            exchange: req.exchange,
            asset_type: req.asset_type,
            timeframe: req.timeframe,
        }
    }
}

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
            exchange: req.exchange,
            asset_type: req.asset_type,
            timeframe: req.timeframe,
        }
    }
}
