use std::collections::HashMap;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use utoipa::ToSchema;

// ── Request ───────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, ToSchema)]
pub struct BacktestRequest {
    /// Strategy key, e.g. `"rsi_mean_rev"`. See `afl/strategy.md` for full list.
    pub strategy: String,

    /// Asset symbol, e.g. `"BTCUSDT"`.
    pub symbol: String,

    /// Strategy-specific parameters as a flat JSON object.
    ///
    /// All values are scalars (`f64` or `u64`), except for array params such as
    /// GMMA period lists. Unknown keys are silently ignored; missing keys use
    /// per-strategy defaults.
    ///
    /// Example (RSI):   `{ "period": 14, "oversold": 30, "overbought": 70 }`
    /// Example (GMMA):  `{ "short_periods": [3,5,8,10,12,15], "long_periods": [30,35,40,45,50,60] }`
    pub params: Option<Value>,

    /// Exit / risk-management overrides.  If omitted the strategy's own
    /// signal-based exit logic is used.
    pub exit: Option<ExitConfig>,

    /// Date range start, inclusive.  Format: `"YYYY-MM-DD"`.
    pub from: Option<String>,

    /// Date range end, inclusive.  Format: `"YYYY-MM-DD"`.
    pub to: Option<String>,

    /// Starting capital in USD (default: 10 000).
    pub initial_capital: Option<f64>,

    /// Commission per trade as a fraction of trade value (default: 0.001 = 0.1%).
    /// Use 0.0 for zero-commission brokers (e.g. Alpaca US stocks).
    pub commission_pct: Option<f64>,

    /// Slippage per trade as a fraction of trade value (default: 0.0005 = 0.05%).
    pub slippage_pct: Option<f64>,

    /// Risk-free annual rate used for Sharpe/Sortino calculation (default: 0.04 = 4%).
    pub risk_free_annual: Option<f64>,

    /// Fraction of equity to allocate per trade (default: 0.95 = 95%).
    /// Lower values leave a cash buffer, e.g. 0.1 for 10%-per-trade Kelly sizing.
    pub position_size_pct: Option<f64>,

    /// If true, drop bars outside regular trading hours for the given exchange.
    /// Requires `exchange` to be set; defaults to `"us"` if omitted.
    pub market_hours_only: Option<bool>,

    /// Exchange / market for hours filtering.
    /// `"us"` → NYSE 09:30–16:00 ET (default)
    /// `"vn"` → HOSE 09:00–11:30 and 13:00–14:45 ICT (UTC+7)
    pub exchange: Option<String>,

    /// Asset class — controls lot sizing.
    /// `"crypto"` → fractional qty (default).
    /// `"stock"`  → whole shares (floor to 1).
    /// `"vn_stock"` → HOSE lots (floor to 100).
    pub asset_type: Option<String>,
}

/// Optional exit / risk-management overrides applied on top of the strategy's
/// own signal-based exit logic.
///
/// The engine evaluates rules every bar while in a position, in order:
/// stop-loss → take-profit → trailing-stop → max-bars-held.
/// The first rule that fires emits a `Close` signal immediately.
#[derive(Debug, Deserialize, Default, ToSchema)]
pub struct ExitConfig {
    /// Cut-loss: close if price falls this fraction below entry price.
    /// E.g. `0.05` cuts at 5 % loss.
    pub stop_loss_pct: Option<f64>,

    /// Take-profit: close if price rises this fraction above entry price.
    /// E.g. `0.10` takes profit at 10 % gain.
    pub take_profit_pct: Option<f64>,

    /// Trailing stop: close if price retreats this fraction from the highest
    /// price seen since entry.  E.g. `0.02` = 2 % trail.
    pub trailing_stop_pct: Option<f64>,

    /// Time-based exit: force-close after this many bars regardless of other
    /// conditions.  Useful for mean-reversion strategies with a holding-period cap.
    pub max_bars_held: Option<usize>,
}

// ── Response ──────────────────────────────────────────────────────────────────

#[derive(Debug, Serialize, ToSchema)]
pub struct BacktestResponse {
    pub strategy: String,
    pub symbol: String,
    /// Echo of the params that were used (empty object `{}` if none supplied).
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

    /// Individual completed trades.
    pub trades: Vec<TradeResponse>,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct TradeResponse {
    pub symbol: String,
    pub side: String,
    pub qty: f64,
    pub entry_price: f64,
    pub exit_price: f64,
    /// ISO 8601 UTC timestamp.
    pub entry_time: String,
    /// ISO 8601 UTC timestamp.
    pub exit_time: String,
    pub pnl: f64,
    pub pnl_pct: f64,
}

#[derive(Debug, Serialize, ToSchema)]
pub struct ErrorResponse {
    pub error: String,
}

// ── CEL backtest request ──────────────────────────────────────────────────────

/// Request body for `POST /api/backtest/cel` — CEL expression strategy.
///
/// Entry/exit are written as CEL expressions with built-in indicator functions:
/// `ema(period)`, `rsi(period)`, `macd(fast, slow, signal)`, `atr(period)`, etc.
/// Prefix `prev_` to access the previous bar's value, e.g. `prev_ema(9)`.
///
/// Example:
/// ```json
/// {
///   "symbol": "BTCUSDT",
///   "entry": "rsi(14) < 30 && close > ema(200)",
///   "exit": "rsi(14) > 70",
///   "sl": 0.05,
///   "tp": 0.15
/// }
/// ```
#[derive(Debug, Deserialize, ToSchema)]
pub struct CelBacktestRequest {
    /// Asset symbol, e.g. `"BTCUSDT"`.
    pub symbol: String,

    /// CEL entry expression.
    pub entry: String,

    /// CEL exit expression.
    pub exit: String,

    /// Take-profit fraction of entry price, e.g. `0.10` = 10 % gain.
    pub tp: Option<f64>,

    /// Stop-loss fraction of entry price, e.g. `0.05` = 5 % loss.
    pub sl: Option<f64>,

    /// Engine-level exit rules (stop-loss / trailing / max-bars) applied on top of the exit expression.
    pub exit_config: Option<ExitConfig>,

    /// Date range start, inclusive.  Format: `"YYYY-MM-DD"`.
    pub from: Option<String>,

    /// Date range end, inclusive.  Format: `"YYYY-MM-DD"`.
    pub to: Option<String>,

    /// Starting capital in USD (default: 10 000).
    pub initial_capital: Option<f64>,

    /// Commission per trade as a fraction of trade value (default: 0.001).
    pub commission_pct: Option<f64>,

    /// Slippage per trade as a fraction of trade value (default: 0.0005).
    pub slippage_pct: Option<f64>,

    /// Risk-free annual rate for Sharpe/Sortino (default: 0.04).
    pub risk_free_annual: Option<f64>,

    /// Fraction of equity per trade (default: 0.95).
    pub position_size_pct: Option<f64>,

    /// Drop bars outside regular market hours.
    pub market_hours_only: Option<bool>,

    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,

    /// Asset class — controls lot sizing.
    /// `"crypto"` → fractional qty (default). `"stock"` → whole shares. `"vn_stock"` → lots of 100.
    pub asset_type: Option<String>,
}

// ── Dynamic backtest request ──────────────────────────────────────────────────

/// Request body for `POST /api/backtest/dynamic` — JSON-defined strategy.
///
/// Define indicators by name, then write entry/exit as rule trees — no Rust needed.
///
/// Example:
/// ```json
/// {
///   "symbol": "BTCUSDT",
///   "indicators": {
///     "rsi14":  { "type": "rsi",  "period": 14 },
///     "ema200": { "type": "ema",  "period": 200 },
///     "macd":   { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
///   },
///   "entry": {
///     "logic": "and",
///     "rules": [
///       { "source": "rsi14",  "field": "value",     "op": "lt",          "value": 30 },
///       { "source": "close",  "field": "value",     "op": "gt",          "compare": "ema200" },
///       { "source": "macd",   "field": "histogram", "op": "cross_above", "value": 0 }
///     ]
///   },
///   "exit": {
///     "logic": "or",
///     "rules": [
///       { "source": "rsi14", "field": "value", "op": "gt", "value": 70 }
///     ]
///   }
/// }
/// ```
#[derive(Debug, Deserialize, ToSchema)]
pub struct DynamicBacktestRequest {
    /// Asset symbol, e.g. `"BTCUSDT"`.
    pub symbol: String,

    /// Named indicator definitions.  Each key becomes the `source` name in rules.
    pub indicators: serde_json::Map<String, serde_json::Value>,

    /// Entry condition tree (`{ "logic": "and"|"or", "rules": [...] }`).
    pub entry: serde_json::Value,

    /// Exit condition tree.
    pub exit: serde_json::Value,

    /// Engine-level exit rules applied on top of the condition tree.
    pub exit_config: Option<ExitConfig>,

    /// Date range start, inclusive.  Format: `"YYYY-MM-DD"`.
    pub from: Option<String>,

    /// Date range end, inclusive.  Format: `"YYYY-MM-DD"`.
    pub to: Option<String>,

    /// Starting capital in USD (default: 10 000).
    pub initial_capital: Option<f64>,

    /// Commission per trade as a fraction of trade value (default: 0.001).
    pub commission_pct: Option<f64>,

    /// Slippage per trade as a fraction of trade value (default: 0.0005).
    pub slippage_pct: Option<f64>,

    /// Risk-free annual rate for Sharpe/Sortino (default: 0.04).
    pub risk_free_annual: Option<f64>,

    /// Fraction of equity per trade (default: 0.95).
    pub position_size_pct: Option<f64>,

    /// Drop bars outside regular market hours.
    pub market_hours_only: Option<bool>,

    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,

    /// Asset class — controls lot sizing.
    /// `"crypto"` → fractional qty (default). `"stock"` → whole shares. `"vn_stock"` → lots of 100.
    pub asset_type: Option<String>,
}

// ── Indicator API ─────────────────────────────────────────────────────────────

/// Request body for `POST /api/indicator`.
#[derive(Debug, Deserialize, ToSchema)]
pub struct IndicatorRequest {
    /// Asset symbol, e.g. `"BTCUSDT"`.
    pub symbol: String,

    /// Date range start, inclusive.  Format: `"YYYY-MM-DD"`.
    pub from: Option<String>,

    /// Date range end, inclusive.  Format: `"YYYY-MM-DD"`.
    pub to: Option<String>,

    /// If `true`, drop bars outside regular market hours.
    pub market_hours_only: Option<bool>,

    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,

    /// One or more indicators to compute.
    pub indicators: Vec<IndicatorConfig>,
}

/// Single indicator specification.
///
/// The `type` field selects the indicator; all other fields are params.
/// Optional `label` overrides the auto-generated series key.
///
/// Examples:
/// ```json
/// { "type": "ema",  "period": 20 }
/// { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
/// { "type": "rsi",  "period": 14, "label": "rsi14" }
/// ```
#[derive(Debug, Deserialize, Clone, ToSchema)]
pub struct IndicatorConfig {
    /// Override series key in the response.  Auto-generated if omitted.
    pub label: Option<String>,

    /// All indicator params including `"type"` — passed directly to `IndicatorBox::from_config`.
    #[serde(flatten)]
    pub config: serde_json::Map<String, Value>,
}

/// One value point in an indicator time series.
#[derive(Debug, Serialize, ToSchema)]
pub struct IndicatorPoint {
    /// Unix timestamp milliseconds.
    pub t: i64,
    /// Named output fields, e.g. `{"value": 45.2}` or `{"macd": 0.5, "signal": 0.3, "histogram": 0.2}`.
    #[serde(flatten)]
    pub fields: HashMap<String, f64>,
}

/// Response for `POST /api/indicator`.
#[derive(Debug, Serialize, ToSchema)]
pub struct IndicatorResponse {
    pub symbol: String,
    /// Total bars loaded (before indicator warmup).
    pub bars: usize,
    /// Map from indicator label → time series (only bars where indicator is ready).
    pub series: HashMap<String, Vec<IndicatorPoint>>,
}
