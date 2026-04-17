use std::collections::HashMap;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use utoipa::{IntoParams, ToSchema};

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
    /// Ignored when `position_size_usd` or `position_size_quantity` is set.
    pub position_size_pct: Option<f64>,

    /// Fixed dollar amount to allocate per trade (e.g. `500.0` = always use $500).
    /// Takes priority over `position_size_pct`.
    /// Ignored when `position_size_quantity` is set.
    pub position_size_usd: Option<f64>,

    /// Fixed quantity to trade per entry (e.g. `0.01` for 0.01 BTC, `100` for 100 shares).
    /// Highest priority — overrides both `position_size_pct` and `position_size_usd`.
    pub position_size_quantity: Option<f64>,

    /// Maximum number of simultaneous open positions (default: 1).
    /// When the limit is reached, new entry signals are rejected until a position closes.
    pub max_positions: Option<usize>,

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

    /// Timeframe subdirectory to load, e.g. `"H1"`, `"M1"`, `"D1"`.
    /// Required when `data_dir` contains multiple timeframes under
    /// `{exchange}/{timeframe}/{symbol}/` layout.
    /// Omit to load all timeframes (legacy flat layout).
    pub timeframe: Option<String>,
}

/// An exit level expressed as either a fixed fraction **or** an ATR multiple.
///
/// JSON examples:
/// - `0.05`          → fixed 5% from entry
/// - `"1.5*atr"`     → 1.5 × ATR(14) — period defaults to 14
/// - `"2*atr(21)"`   → 2 × ATR(21)
#[derive(Debug, Clone, Deserialize, ToSchema)]
#[serde(untagged)]
pub enum ExitLevel {
    /// Fixed fraction of entry price (e.g. `0.05` = 5%).
    Pct(f64),
    /// ATR multiple expression: `"N*atr"` or `"N*atr(P)"`.
    Expr(String),
}

impl ExitLevel {
    /// Parse an ATR-multiple expression → `(multiplier, period)`.
    /// Returns `None` if the string is not a recognised ATR expression.
    pub fn as_atr(&self) -> Option<(f64, usize)> {
        let s = match self {
            Self::Expr(s) => s.trim(),
            Self::Pct(_)  => return None,
        };
        // Accept "N*atr" or "N*atr(P)", case-insensitive
        let s_low = s.to_lowercase();
        let base = s_low.strip_suffix(')')
            .and_then(|b| {
                let idx = b.find("*atr(")?;
                Some((&b[..idx], &b[idx + 5..]))
            });

        if let Some((mult_str, period_str)) = base {
            let mult   = mult_str.trim().parse::<f64>().ok()?;
            let period = period_str.trim().parse::<usize>().ok()?;
            return Some((mult, period));
        }

        // No parentheses — "N*atr"
        if let Some(mult_str) = s_low.strip_suffix("*atr") {
            let mult = mult_str.trim().parse::<f64>().ok()?;
            return Some((mult, 14));
        }

        None
    }

    /// Fixed-pct value, or `None` when this is an ATR expression.
    pub fn as_pct(&self) -> Option<f64> {
        match self { Self::Pct(v) => Some(*v), Self::Expr(_) => None }
    }
}

/// Exit / risk-management overrides applied on top of strategy signal logic.
///
/// `tp` and `sl` accept either a fixed fraction **or** an ATR-multiple expression:
/// ```json
/// { "sl": 0.05, "tp": "3*atr(14)", "max_bars": 50 }
/// ```
///
#[derive(Debug, Clone, Deserialize, Default, ToSchema)]
pub struct ExitConfig {
    /// Take-profit: close when price rises to this level above entry.
    /// Fixed: `0.10` (10%). ATR: `"3*atr(14)"` (3× ATR(14) above entry).
    pub tp: Option<ExitLevel>,

    /// Stop-loss: close when price falls to this level below entry.
    /// Fixed: `0.05` (5%). ATR: `"1.5*atr"` (1.5× ATR(14) below entry).
    pub sl: Option<ExitLevel>,

    /// Time-based exit: force-close after this many bars in position.
    pub max_bars: Option<usize>,
}

// ── Response ──────────────────────────────────────────────────────────────────

/// One point on the equity or drawdown curve.
#[derive(Debug, Serialize, ToSchema)]
pub struct CurvePoint {
    /// Unix timestamp in milliseconds (UTC).
    pub t: i64,
    pub v: f64,
}

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

    /// Bar-by-bar equity curve: `[{t, v}]` where `v` is total equity in currency.
    pub equity_curve: Vec<CurvePoint>,

    /// Bar-by-bar drawdown series: `[{t, v}]` where `v` is drawdown as fraction (e.g. -0.15 = -15%).
    pub drawdown_curve: Vec<CurvePoint>,

    /// Per-indicator time series — only populated for CEL and Dynamic strategies.
    /// Keys: `"ema_9"`, `"rsi_14"` (CEL) or `"rsi14.value"`, `"macd.histogram"` (Dynamic).
    /// Each value is a `[{t, v}]` series aligned to bar timestamps.
    #[serde(skip_serializing_if = "std::collections::HashMap::is_empty")]
    pub indicator_series: std::collections::HashMap<String, Vec<CurvePoint>>,

    // Advanced risk metrics
    pub var_95: f64,
    pub cvar_95: f64,
    pub omega_ratio: f64,
    pub tail_ratio: f64,
    pub recovery_factor: f64,

    // Rolling metrics (window = 30 bars)
    pub rolling_sharpe: Vec<f64>,
    pub rolling_drawdown: Vec<f64>,

    /// Detected bar timeframe, e.g. `"M1"`, `"H1"`, `"D1"`.
    pub timeframe: String,

    /// % of total bars the strategy held an open position.
    pub exposure_pct: f64,

    /// Market regime breakdown — `None` if regime detector didn't warm up.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub regime_summary: Option<RegimeSummaryResponse>,

    /// Buy-and-hold benchmark on the same period and asset.
    /// `None` when fewer than 2 bars are available.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub benchmark: Option<BuyHoldBenchmarkResponse>,
}

/// Market regime breakdown from a completed backtest.
#[derive(Debug, Serialize, ToSchema)]
pub struct RegimeSummaryResponse {
    /// % of detected bars in Trending regime.
    pub trending_pct: f64,
    /// % of detected bars in Ranging regime.
    pub ranging_pct: f64,
    /// % of detected bars in Neutral regime.
    pub neutral_pct: f64,
    /// % of detected bars in High volatility regime.
    pub high_vol_pct: f64,
    /// % of detected bars in Low volatility regime.
    pub low_vol_pct: f64,
    /// On-change regime transitions: `[timestamp_ms, label]` pairs.
    pub changes: Vec<(i64, String)>,
}

/// Buy-and-hold benchmark stats — buy at first bar open, hold to last bar close.
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
pub struct TradeResponse {
    pub symbol: String,
    pub side: String,
    pub qty: f64,
    pub entry_price: f64,
    pub exit_price: f64,
    /// Unix timestamp milliseconds — use for chart marker alignment.
    pub entry_ts: i64,
    /// Unix timestamp milliseconds — use for chart marker alignment.
    pub exit_ts: i64,
    /// ISO 8601 UTC timestamp (human-readable).
    pub entry_time: String,
    /// ISO 8601 UTC timestamp (human-readable).
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
/// Entry/exit are CEL expressions with built-in indicator functions.
///
/// Bar fields in scope: `open`, `high`, `low`, `close`, `volume`, `peak`, `entry_price`.
/// Indicators: `ema(N)`, `rsi(N)`, `macd_hist(N)`, `atr(N)`, `bb_upper(N)`, etc.
/// MTF prefix: `H1.ema(200)`, `M15.rsi(14)`, etc.
/// Previous bar: `prev_ema(9)`, `prev_rsi(14)`, etc.
///
/// Example — ATR-based exits + Heiken Ashi:
/// ```json
/// {
///   "symbol": "BTCUSDT",
///   "entry_expr": "rsi(14) < 30 && close > ema(200)",
///   "exit_expr":  "rsi(14) > 70 || close < peak - 2*atr(14)",
///   "exit": { "sl": "1.5*atr(14)", "tp": 0.20, "max_bars": 100 },
///   "candle_type": "heiken_ashi",
///   "from": "2023-01-01", "to": "2024-01-01"
/// }
/// ```
#[derive(Debug, Deserialize, ToSchema)]
pub struct CelBacktestRequest {
    /// Asset symbol, e.g. `"BTCUSDT"`.
    pub symbol: String,

    /// CEL entry expression, e.g. `"rsi(14) < 30 && close > ema(200)"`.
    pub entry_expr: String,

    /// CEL exit expression, e.g. `"rsi(14) > 70 || close < peak * 0.97"`.
    /// Use `"false"` to rely solely on `exit` config.
    /// Variables: `open` `high` `low` `close` `volume` `peak` `entry_price` + all indicators.
    pub exit_expr: String,

    // ── Exit / risk params ────────────────────────────────────────────────────

    /// Exit rules: `tp`, `sl` (fixed % or ATR expression), `max_bars`.
    pub exit: Option<ExitConfig>,

    /// Candle transform applied before evaluating expressions and indicators.
    /// `"raw"` (default) · `"heiken_ashi"` · `"smooth_ha"`.
    pub candle_type: Option<String>,

    /// Smoothing period for `"smooth_ha"` candle type (default: 2).
    pub ha_smooth: Option<usize>,

    // ── Date range ────────────────────────────────────────────────────────────

    /// Date range start, inclusive.  Format: `"YYYY-MM-DD"`.
    pub from: Option<String>,

    /// Date range end, inclusive.  Format: `"YYYY-MM-DD"`.
    pub to: Option<String>,

    // ── Engine params ─────────────────────────────────────────────────────────

    /// Starting capital in USD (default: 10 000).
    pub initial_capital: Option<f64>,

    /// Commission per trade as a fraction of trade value (default: 0.001).
    pub commission_pct: Option<f64>,

    /// Slippage per trade as a fraction of trade value (default: 0.0005).
    pub slippage_pct: Option<f64>,

    /// Risk-free annual rate for Sharpe/Sortino (default: 0.04).
    pub risk_free_annual: Option<f64>,

    /// Fraction of equity per trade (default: 0.95).
    /// Ignored when `position_size_usd` or `position_size_quantity` is set.
    pub position_size_pct: Option<f64>,

    /// Fixed dollar amount per trade (e.g. `500.0`).
    /// Takes priority over `position_size_pct`.
    pub position_size_usd: Option<f64>,

    /// Fixed quantity per trade (e.g. `0.01` BTC, `100` shares).
    /// Highest priority — overrides both `position_size_pct` and `position_size_usd`.
    pub position_size_quantity: Option<f64>,

    /// Maximum simultaneous open positions (default: 1).
    pub max_positions: Option<usize>,

    /// Drop bars outside regular market hours.
    pub market_hours_only: Option<bool>,

    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,

    /// Asset class — controls lot sizing.
    /// `"crypto"` → fractional qty (default). `"stock"` → whole shares. `"vn_stock"` → lots of 100.
    pub asset_type: Option<String>,

    /// Timeframe subdirectory, e.g. `"H1"`, `"M1"`, `"D1"`. See `BacktestRequest.timeframe`.
    pub timeframe: Option<String>,
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

    /// Exit rules: `tp`, `sl` (fixed % or ATR expression), `max_bars`.
    pub exit_rules: Option<ExitConfig>,

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
    /// Ignored when `position_size_usd` or `position_size_quantity` is set.
    pub position_size_pct: Option<f64>,

    /// Fixed dollar amount per trade (e.g. `500.0`).
    /// Takes priority over `position_size_pct`.
    pub position_size_usd: Option<f64>,

    /// Fixed quantity per trade (e.g. `0.01` BTC, `100` shares).
    /// Highest priority — overrides both `position_size_pct` and `position_size_usd`.
    pub position_size_quantity: Option<f64>,

    /// Maximum simultaneous open positions (default: 1).
    pub max_positions: Option<usize>,

    /// Drop bars outside regular market hours.
    pub market_hours_only: Option<bool>,

    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,

    /// Asset class — controls lot sizing.
    /// `"crypto"` → fractional qty (default). `"stock"` → whole shares. `"vn_stock"` → lots of 100.
    pub asset_type: Option<String>,

    /// Timeframe subdirectory, e.g. `"H1"`, `"M1"`, `"D1"`. See `BacktestRequest.timeframe`.
    pub timeframe: Option<String>,
}

// ── Data API ──────────────────────────────────────────────────────────────────

/// A single OHLCV bar as returned by `GET /api/data/{symbol}`.
#[derive(Debug, Serialize, ToSchema)]
pub struct BarRecord {
    /// Unix timestamp in milliseconds (UTC).
    pub t: i64,
    pub o: f64,
    pub h: f64,
    pub l: f64,
    pub c: f64,
    pub v: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub vwap: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub n: Option<i64>,
}

/// Response envelope for `GET /api/data/{symbol}`.
#[derive(Debug, Serialize, ToSchema)]
pub struct DataResponse {
    pub symbol: String,
    pub count: usize,
    pub bars: Vec<BarRecord>,
}

/// Query parameters for `GET /api/data/{symbol}/latest`.
#[derive(Debug, Deserialize, IntoParams)]
pub struct LatestQuery {
    /// Number of bars to return (default: 500, max: 5000).
    pub n: Option<usize>,
    /// If `true`, only include bars within regular market hours.
    pub market_hours_only: Option<bool>,
    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,
    /// Timeframe subdirectory, e.g. `"H1"`, `"M1"`, `"D1"`. See `BacktestRequest.timeframe`.
    pub timeframe: Option<String>,
}

/// Query parameters for `GET /api/data/{symbol}`.
#[derive(Debug, Deserialize, IntoParams)]
pub struct DataQuery {
    /// Start date inclusive, format `YYYY-MM-DD`.
    pub from: Option<String>,
    /// End date inclusive, format `YYYY-MM-DD`.
    pub to: Option<String>,
    /// Maximum number of bars to return (default: 5000, max: 50000).
    pub limit: Option<usize>,
    /// If `true`, only include bars within regular market hours.
    pub market_hours_only: Option<bool>,
    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,
    /// Timeframe subdirectory, e.g. `"H1"`, `"M1"`, `"D1"`. See `BacktestRequest.timeframe`.
    pub timeframe: Option<String>,
}

// ── Unified data API ──────────────────────────────────────────────────────────

/// Candle-section options inside `UnifiedDataRequest`.
///
/// Presence of this object signals that OHLCV bars should be included in the response.
/// Absence means "skip bars" (indicator-only mode).
///
/// **`limit` semantics:**
/// - with top-level `from`/`to` → first N bars of the range
/// - without `from`/`to` → *last* N bars (equivalent to `GET /api/data/{symbol}/latest`)
///
/// **`candle_type`** is applied before indicators are computed, so both bars and
/// indicator series in the response reflect the same candle transform.
#[derive(Debug, Deserialize, ToSchema)]
pub struct CandlesQuery {
    /// Max bars to return. Without `from`/`to`, returns the *last* N bars.
    pub limit: Option<usize>,

    /// Candle transform applied to bars before indicators are computed.
    /// `"raw"` (default) · `"heiken_ashi"` · `"smooth_ha"`
    pub candle_type: Option<String>,

    /// EMA smoothing period for `"smooth_ha"` (default: 2, minimum: 2).
    pub ha_smooth: Option<usize>,
}

/// Candles section of `UnifiedDataResponse`.
#[derive(Debug, Serialize, ToSchema)]
pub struct CandlesResult {
    /// Number of bars in this response (after limit).
    pub count: usize,
    pub bars: Vec<BarRecord>,
}

/// Request body for `POST /api/data/{symbol}` — unified OHLCV + indicator query.
///
/// Two independent sections control what is returned:
/// - `candles` present → include OHLCV bars (with optional limit)
/// - `indicators` present and non-empty → compute and include indicator series
/// - both present → bars + series in one round-trip (chart init)
/// - only `indicators` → indicator-only (caller already has bars)
/// - only `candles` → bars-only (equivalent to `GET /api/data/{symbol}`)
#[derive(Debug, Deserialize, ToSchema)]
pub struct UnifiedDataRequest {
    /// Date range start, inclusive.  Format: `"YYYY-MM-DD"`.
    pub from: Option<String>,

    /// Date range end, inclusive.  Format: `"YYYY-MM-DD"`.
    pub to: Option<String>,

    /// Timeframe subdirectory, e.g. `"H1"`, `"M1"`, `"D1"`. See `BacktestRequest.timeframe`.
    pub timeframe: Option<String>,

    /// Drop bars outside regular market hours (default: false).
    pub market_hours_only: Option<bool>,

    /// Exchange for market-hours filtering: `"us"` (default) or `"vn"`.
    pub exchange: Option<String>,

    /// Candle options. Presence = include OHLCV bars in response.
    pub candles: Option<CandlesQuery>,

    /// Indicators to compute over the loaded bars.
    /// Presence with non-empty list = include indicator series in response.
    pub indicators: Option<Vec<IndicatorConfig>>,
}

/// Response for `POST /api/data/{symbol}`.
#[derive(Debug, Serialize, ToSchema)]
pub struct UnifiedDataResponse {
    pub symbol: String,
    /// OHLCV bars — present when `candles` was requested.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub candles: Option<CandlesResult>,
    /// Indicator series keyed by label — present when `indicators` were requested.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub indicators: Option<HashMap<String, Vec<IndicatorPoint>>>,
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

    /// Timeframe subdirectory, e.g. `"H1"`, `"M1"`, `"D1"`. See `BacktestRequest.timeframe`.
    pub timeframe: Option<String>,
}

/// Single indicator specification.
///
/// Specify the indicator either via JSON params (`"type"` + params) **or** via a
/// CEL-style expression string (`"cel"`).  CEL supports MTF with `TF.func()` syntax.
///
/// Examples:
/// ```json
/// { "type": "ema",  "period": 20 }
/// { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
/// { "type": "rsi",  "period": 14, "label": "rsi14" }
/// { "cel": "H1.ema(200)", "label": "h1_ema200" }
/// { "cel": "rsi(14)" }
/// ```
#[derive(Debug, Deserialize, Clone, ToSchema)]
pub struct IndicatorConfig {
    /// Override series key in the response.  Auto-generated if omitted.
    pub label: Option<String>,

    /// CEL-style indicator expression, e.g. `"H1.ema(200)"` or `"rsi(14)"`.
    /// When set, `config` fields are ignored (except `label`).
    /// Supports MTF prefix: `M1` `M5` `M15` `M30` `H1` `H2` `H4` `H6` `H8` `H12` `D1` `W1`.
    pub cel: Option<String>,

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
