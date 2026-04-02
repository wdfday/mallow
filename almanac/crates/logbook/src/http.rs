//! Axum HTTP API for logbook — `/api/backtest`, `/api/strategies`, `/api/symbols`,
//! `/api/data/{symbol}`, `/health`, `/docs`.

use std::collections::BTreeSet;
use std::path::PathBuf;
use std::sync::Arc;

use axum::{
    extract::{Path, Query, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::{get, post},
    Json, Router,
};
use serde::{Deserialize, Serialize};
use tokio::sync::Semaphore;
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;
use utoipa::openapi::security::{HttpAuthScheme, HttpBuilder, SecurityScheme};
use utoipa::{IntoParams, Modify, OpenApi, ToSchema};
use utoipa_swagger_ui::SwaggerUi;
use walkdir::WalkDir;

use crate::data::{find_parquet_files, load_bars, parse_date_ms};
use crate::runner;
use crate::types::{
    BacktestRequest, BacktestResponse, CelBacktestRequest, DynamicBacktestRequest,
    ErrorResponse, ExitConfig, TradeResponse,
    IndicatorRequest, IndicatorResponse, IndicatorConfig,
};

// ── Strategy catalogue ────────────────────────────────────────────────────────

pub const STRATEGY_KEYS: &[&str] = &[
    "ma_crossover",
    "triple_ema",
    "hma_crossover",
    "rsi_mean_rev",
    "macd_crossover",
    "macd_ma",
    "waddah_attar",
    "stochastic_crossover",
    "stochastic_dk",
    "range_rover",
    "reversal_catcher",
    "atr_trailing",
    "volatility_squeezer",
    "volatility_vanguard",
    "volatility_ratio",
    "bollinger_macd",
    "bb_squeeze",
    "mean_reversion",
    "bb_keltner_squeeze",
    "dmi_adx",
    "wolfstein",
    "trend_transition",
    "swing_trader",
    "oscillator_overlord",
    "equilibrium_explorer",
    "trend_follower",
    "ma_pullback",
    "cci_reversal",
    "supertrend",
    "supertrend_macd",
    "parabolic_sar",
    "heiken_ashi_color",
    "heiken_ashi_breakout",
    "heiken_ashi_harmonizer",
    "gmma_crossover",
    "ichimoku_cloud",
    "ichimoku_cross",
    "scalping_ema",
    "dema_crossover",
    "donchian_breakout",
    "elder_ray",
    "aroon_trend",
    "chandelier_exit",
    "vwap_bounce",
    "vwap_trend",
    "momentum_roc",
    "dual_momentum",
    "price_action_swing",
    "orb_breakout",
    "highest_breakout",
    "keltner_breakout",
    "mfi_trend",
    "mfi_revert",
    "roc",
    "kst",
    "trix",
    "alligator",
    "rwi",
    "pattern_breakout",
    "kama",
    "tsi",
    "stoch_rsi",
    "chop_filter",
    "connors_rsi",
    "kdj",
    "ao",
    "tema_crossover",
    "pixel_3",
    "cel",
    "dynamic",
    "pixel_3",
];

// ── Data types ────────────────────────────────────────────────────────────────

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
    /// Volume-weighted average price (if available in source data).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub vwap: Option<f64>,
    /// Number of transactions (if available).
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
}

// ── App state ─────────────────────────────────────────────────────────────────

#[derive(Clone)]
pub struct AppState {
    pub data_dir: Arc<PathBuf>,
    /// Limits concurrent CPU-bound backtest tasks to protect VPS RAM/CPU.
    pub semaphore: Arc<Semaphore>,
}

// ── OpenAPI doc ───────────────────────────────────────────────────────────────

struct SecurityAddon;

impl Modify for SecurityAddon {
    fn modify(&self, openapi: &mut utoipa::openapi::OpenApi) {
        let components = openapi.components.get_or_insert_with(Default::default);
        components.add_security_scheme(
            "BearerAuth",
            SecurityScheme::Http(
                HttpBuilder::new()
                    .scheme(HttpAuthScheme::Bearer)
                    .bearer_format("JWT")
                    .build(),
            ),
        );
    }
}

#[derive(OpenApi)]
#[openapi(
    info(
        title = "Logbook Backtest API",
        version = "0.1.0",
        description = "Event-driven backtesting engine — run strategies on historical OHLCV data."
    ),
    modifiers(&SecurityAddon),
    paths(
        health,
        list_strategies,
        list_symbols,
        get_data,
        run_backtest,
        run_backtest_cel,
        run_backtest_dynamic,
        compute_indicator,
    ),
    components(schemas(
        BacktestRequest,
        CelBacktestRequest,
        DynamicBacktestRequest,
        ExitConfig,
        BacktestResponse,
        TradeResponse,
        ErrorResponse,
        BarRecord,
        DataResponse,
        IndicatorRequest,
        IndicatorConfig,
        IndicatorResponse,
    )),
    tags(
        (name = "backtest", description = "Backtest execution endpoints"),
        (name = "data",     description = "Market data endpoints"),
        (name = "meta",     description = "API metadata endpoints"),
    )
)]
pub struct ApiDoc;

// ── Router ────────────────────────────────────────────────────────────────────

pub fn router(state: AppState) -> Router {
    let api = Router::new()
        .route("/health", get(health))
        .route("/api/strategies", get(list_strategies))
        .route("/api/symbols", get(list_symbols))
        .route("/api/data/{symbol}", get(get_data))
        .route("/api/backtest", post(run_backtest))
        .route("/api/backtest/cel", post(run_backtest_cel))
        .route("/api/backtest/dynamic", post(run_backtest_dynamic))
        .route("/api/indicator", post(compute_indicator))
        .with_state(state);

    Router::new()
        .merge(SwaggerUi::new("/swagger/logbook").url("/api-doc/openapi.json", ApiDoc::openapi()))
        .merge(api)
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http())
}

// ── Handlers ──────────────────────────────────────────────────────────────────

/// Simple health check.
#[utoipa::path(
    get,
    path = "/health",
    tag = "meta",
    responses(
        (status = 200, description = "Service is healthy", body = serde_json::Value),
    )
)]
async fn health() -> impl IntoResponse {
    Json(serde_json::json!({ "ok": true }))
}

/// List available strategy keys.
///
/// Returns a JSON array of strategy name strings accepted by `POST /api/backtest`.
#[utoipa::path(
    get,
    path = "/api/strategies",
    tag = "meta",
    security(("BearerAuth" = [])),
    responses(
        (status = 200, description = "List of strategy keys", body = Vec<String>),
    )
)]
async fn list_strategies() -> impl IntoResponse {
    Json(STRATEGY_KEYS)
}

/// List symbols that have data available in the data directory.
///
/// Walks the data directory and returns every folder name that contains at least
/// one `.parquet` file (deduplicated, sorted alphabetically).
#[utoipa::path(
    get,
    path = "/api/symbols",
    tag = "data",
    security(("BearerAuth" = [])),
    responses(
        (status = 200, description = "Sorted list of available symbols", body = Vec<String>),
    )
)]
async fn list_symbols(State(state): State<AppState>) -> impl IntoResponse {
    let data_dir = Arc::clone(&state.data_dir);
    let symbols = tokio::task::spawn_blocking(move || discover_symbols(&data_dir))
        .await
        .unwrap_or_default();
    Json(symbols)
}

/// Load OHLCV bars for a symbol.
///
/// Reads parquet files from the data directory filtered by date range.
/// Results are capped at `limit` bars (default 5 000, max 50 000).
#[utoipa::path(
    get,
    path = "/api/data/{symbol}",
    tag = "data",
    security(("BearerAuth" = [])),
    params(
        ("symbol" = String, Path, description = "Asset symbol, e.g. `BTCUSDT` or `NVDA`"),
        DataQuery,
    ),
    responses(
        (status = 200, description = "OHLCV bars",                          body = DataResponse),
        (status = 400, description = "Symbol not found or bad date range",  body = ErrorResponse),
    )
)]
async fn get_data(
    State(state): State<AppState>,
    Path(symbol): Path<String>,
    Query(q): Query<DataQuery>,
) -> Response {
    let data_dir = Arc::clone(&state.data_dir);
    let limit = q.limit.unwrap_or(5_000).min(50_000);
    let market_hours_only = q.market_hours_only.unwrap_or(false);
    let exchange = q.exchange.clone().unwrap_or_else(|| "us".to_string());

    let from_ms = q.from.as_deref().and_then(parse_date_ms);
    let to_ms = q.to.as_deref().and_then(|s| {
        parse_date_ms(s).map(|ms| ms + 86_400_000 - 1) // inclusive end-of-day
    });

    let symbol_clone = symbol.clone();
    let result = tokio::task::spawn_blocking(move || {
        let files = find_parquet_files(&data_dir, &symbol_clone);
        load_bars(
            &files,
            &symbol_clone,
            from_ms,
            to_ms,
            market_hours_only,
            &exchange,
        )
    })
    .await;

    match result {
        Ok(Ok(mut feed)) => {
            use alm_data::BarFeed;
            let mut bars: Vec<BarRecord> = Vec::with_capacity(limit.min(feed.len()));
            while let Some(bar) = feed.next() {
                bars.push(BarRecord {
                    t: bar.timestamp,
                    o: bar.open,
                    h: bar.high,
                    l: bar.low,
                    c: bar.close,
                    v: bar.volume,
                    vwap: bar.vwap,
                    n: bar.transactions,
                });
                if bars.len() >= limit {
                    break;
                }
            }
            let count = bars.len();
            (
                StatusCode::OK,
                Json(DataResponse {
                    symbol,
                    count,
                    bars,
                }),
            )
                .into_response()
        }

        Ok(Err(e)) => {
            tracing::warn!("data load error for {symbol}: {:#}", e);
            (
                StatusCode::BAD_REQUEST,
                Json(ErrorResponse {
                    error: e.to_string(),
                }),
            )
                .into_response()
        }

        Err(e) => {
            tracing::error!("spawn_blocking panicked: {}", e);
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(ErrorResponse {
                    error: "internal error".into(),
                }),
            )
                .into_response()
        }
    }
}

/// Run a backtest with a named strategy.
///
/// Concurrent requests beyond the semaphore limit receive `429`.
#[utoipa::path(
    post,
    path = "/api/backtest",
    tag = "backtest",
    security(("BearerAuth" = [])),
    request_body(
        content = BacktestRequest,
        examples(
            ("RSI — BTCUSDT" = (
                summary = "RSI mean reversion on BTCUSDT 1m (2022–2024)",
                value = json!({
                    "strategy": "rsi_mean_rev",
                    "symbol": "BTCUSDT",
                    "from": "2022-06-01",
                    "to": "2024-01-01",
                    "params": { "period": 14, "oversold": 30, "overbought": 70 },
                    "exit": { "stop_loss_pct": 0.05, "trailing_stop_pct": 0.03 },
                    "initial_capital": 10000.0,
                    "commission_pct": 0.001
                })
            )),
            ("MACD — AAPL" = (
                summary = "MACD crossover on AAPL 1min (2024–2026)",
                value = json!({
                    "strategy": "macd_crossover",
                    "symbol": "AAPL",
                    "from": "2024-04-01",
                    "to": "2026-01-01",
                    "params": { "fast": 12, "slow": 26, "signal": 9 },
                    "market_hours_only": true,
                    "exchange": "us",
                    "commission_pct": 0.0,
                    "initial_capital": 10000.0
                })
            )),
            ("EMA crossover — FPT" = (
                summary = "EMA 9/21 crossover on FPT (VN stock)",
                value = json!({
                    "strategy": "ma_crossover",
                    "symbol": "FPT",
                    "from": "2024-01-01",
                    "to": "2026-01-01",
                    "params": { "fast": 9, "slow": 21 },
                    "market_hours_only": true,
                    "exchange": "vn",
                    "commission_pct": 0.0015,
                    "initial_capital": 100000000.0
                })
            ))
        )
    ),
    responses(
        (status = 200, description = "Backtest completed successfully",             body = BacktestResponse),
        (status = 400, description = "Invalid request or unknown strategy",         body = ErrorResponse),
        (status = 429, description = "Too many concurrent backtests — retry later", body = ErrorResponse),
        (status = 500, description = "Internal engine error",                       body = ErrorResponse),
    )
)]
async fn run_backtest(State(state): State<AppState>, Json(req): Json<BacktestRequest>) -> Response {
    let _permit = match state.semaphore.try_acquire() {
        Ok(p) => p,
        Err(_) => {
            return (
                StatusCode::TOO_MANY_REQUESTS,
                Json(ErrorResponse {
                    error: "too many concurrent backtests — please retry shortly".into(),
                }),
            )
                .into_response();
        }
    };

    tracing::info!(
        strategy = %req.strategy,
        symbol   = %req.symbol,
        from     = ?req.from,
        to       = ?req.to,
        "HTTP backtest request"
    );

    let data_dir = Arc::clone(&state.data_dir);
    let result = tokio::task::spawn_blocking(move || runner::run(req, &data_dir)).await;
    backtest_response(result)
}

/// Run a backtest with a CEL expression strategy.
///
/// Entry and exit are CEL expressions with built-in indicator functions.
/// Concurrent requests beyond the semaphore limit receive `429`.
#[utoipa::path(
    post,
    path = "/api/backtest/cel",
    tag = "backtest",
    security(("BearerAuth" = [])),
    request_body(
        content = CelBacktestRequest,
        examples(
            ("RSI + EMA — BTCUSDT" = (
                summary = "RSI oversold + price above EMA200 on BTCUSDT 1m",
                value = json!({
                    "symbol": "BTCUSDT",
                    "entry": "rsi(14) < 30 && close > ema(200)",
                    "exit": "rsi(14) > 70",
                    "sl": 0.05,
                    "tp": 0.15,
                    "from": "2022-06-01",
                    "to": "2024-01-01",
                    "initial_capital": 10000.0,
                    "commission_pct": 0.001
                })
            )),
            ("EMA crossover — AAPL" = (
                summary = "EMA 9/21 crossover signal on AAPL 1min",
                value = json!({
                    "symbol": "AAPL",
                    "entry": "prev_ema(9) < prev_ema(21) && ema(9) > ema(21)",
                    "exit": "prev_ema(9) > prev_ema(21) && ema(9) < ema(21)",
                    "sl": 0.04,
                    "from": "2024-04-01",
                    "to": "2026-01-01",
                    "market_hours_only": true,
                    "exchange": "us",
                    "commission_pct": 0.0,
                    "initial_capital": 10000.0,
                    "exit_config": { "trailing_stop_pct": 0.03 }
                })
            )),
            ("MACD histogram — FPT" = (
                summary = "MACD histogram cross zero on FPT (VN)",
                value = json!({
                    "symbol": "FPT",
                    "entry": "prev_macd(12,26,9).histogram < 0 && macd(12,26,9).histogram > 0",
                    "exit": "prev_macd(12,26,9).histogram > 0 && macd(12,26,9).histogram < 0",
                    "sl": 0.05,
                    "from": "2024-01-01",
                    "to": "2026-01-01",
                    "market_hours_only": true,
                    "exchange": "vn",
                    "commission_pct": 0.0015,
                    "initial_capital": 100000000.0
                })
            ))
        )
    ),
    responses(
        (status = 200, description = "Backtest completed successfully",             body = BacktestResponse),
        (status = 400, description = "Invalid expressions or symbol not found",     body = ErrorResponse),
        (status = 429, description = "Too many concurrent backtests — retry later", body = ErrorResponse),
        (status = 500, description = "Internal engine error",                       body = ErrorResponse),
    )
)]
async fn run_backtest_cel(
    State(state): State<AppState>,
    Json(req): Json<CelBacktestRequest>,
) -> Response {
    let _permit = match state.semaphore.try_acquire() {
        Ok(p) => p,
        Err(_) => {
            return (
                StatusCode::TOO_MANY_REQUESTS,
                Json(ErrorResponse {
                    error: "too many concurrent backtests — please retry shortly".into(),
                }),
            )
                .into_response();
        }
    };

    tracing::info!(symbol = %req.symbol, "CEL backtest request");

    let base: BacktestRequest = req.into();
    let data_dir = Arc::clone(&state.data_dir);
    let result = tokio::task::spawn_blocking(move || runner::run(base, &data_dir)).await;
    backtest_response(result)
}

/// Run a backtest with a JSON-defined (dynamic) strategy.
///
/// Define indicators by name and wire them into entry/exit condition trees.
/// Concurrent requests beyond the semaphore limit receive `429`.
#[utoipa::path(
    post,
    path = "/api/backtest/dynamic",
    tag = "backtest",
    security(("BearerAuth" = [])),
    request_body(
        content = DynamicBacktestRequest,
        examples(
            ("RSI + EMA — BTCUSDT" = (
                summary = "RSI oversold + price above EMA200 on BTCUSDT 1m",
                value = json!({
                    "symbol": "BTCUSDT",
                    "from": "2022-06-01",
                    "to": "2024-01-01",
                    "initial_capital": 10000.0,
                    "commission_pct": 0.001,
                    "indicators": {
                        "rsi14":  { "type": "rsi", "period": 14 },
                        "ema200": { "type": "ema", "period": 200 }
                    },
                    "entry": {
                        "logic": "and",
                        "rules": [
                            { "source": "rsi14", "field": "value", "op": "lt", "value": 30 },
                            { "source": "close", "field": "value", "op": "gt", "compare": "ema200" }
                        ]
                    },
                    "exit": {
                        "logic": "or",
                        "rules": [
                            { "source": "rsi14", "field": "value", "op": "gt", "value": 70 }
                        ]
                    },
                    "exit_config": { "stop_loss_pct": 0.05, "trailing_stop_pct": 0.03 }
                })
            )),
            ("MACD + RSI — AAPL" = (
                summary = "MACD histogram cross với RSI confirmation trên AAPL 1min",
                value = json!({
                    "symbol": "AAPL",
                    "from": "2024-04-01",
                    "to": "2026-01-01",
                    "initial_capital": 10000.0,
                    "commission_pct": 0.0,
                    "market_hours_only": true,
                    "exchange": "us",
                    "indicators": {
                        "macd":  { "type": "macd", "fast": 12, "slow": 26, "signal": 9 },
                        "rsi14": { "type": "rsi",  "period": 14 }
                    },
                    "entry": {
                        "logic": "and",
                        "rules": [
                            { "source": "macd",  "field": "histogram", "op": "cross_above", "value": 0 },
                            { "source": "rsi14", "field": "value",     "op": "lt",          "value": 60 }
                        ]
                    },
                    "exit": {
                        "logic": "or",
                        "rules": [
                            { "source": "macd",  "field": "histogram", "op": "cross_below", "value": 0 },
                            { "source": "rsi14", "field": "value",     "op": "gt",          "value": 75 }
                        ]
                    }
                })
            )),
            ("BB + RSI — FPT" = (
                summary = "Bollinger Band squeeze với RSI trên FPT (VN)",
                value = json!({
                    "symbol": "FPT",
                    "from": "2024-01-01",
                    "to": "2026-01-01",
                    "initial_capital": 100000000.0,
                    "commission_pct": 0.0015,
                    "market_hours_only": true,
                    "exchange": "vn",
                    "indicators": {
                        "rsi14": { "type": "rsi",  "period": 14 },
                        "bb20":  { "type": "bbands", "period": 20, "std_dev": 2.0 }
                    },
                    "entry": {
                        "logic": "and",
                        "rules": [
                            { "source": "rsi14", "field": "value",  "op": "lt", "value": 35 },
                            { "source": "close", "field": "value",  "op": "lt", "compare": "bb20.lower" }
                        ]
                    },
                    "exit": {
                        "logic": "or",
                        "rules": [
                            { "source": "rsi14", "field": "value", "op": "gt", "value": 65 },
                            { "source": "close", "field": "value", "op": "gt", "compare": "bb20.upper" }
                        ]
                    },
                    "exit_config": { "stop_loss_pct": 0.05 }
                })
            ))
        )
    ),
    responses(
        (status = 200, description = "Backtest completed successfully",             body = BacktestResponse),
        (status = 400, description = "Invalid config or symbol not found",          body = ErrorResponse),
        (status = 429, description = "Too many concurrent backtests — retry later", body = ErrorResponse),
        (status = 500, description = "Internal engine error",                       body = ErrorResponse),
    )
)]
async fn run_backtest_dynamic(
    State(state): State<AppState>,
    Json(req): Json<DynamicBacktestRequest>,
) -> Response {
    let _permit = match state.semaphore.try_acquire() {
        Ok(p) => p,
        Err(_) => {
            return (
                StatusCode::TOO_MANY_REQUESTS,
                Json(ErrorResponse {
                    error: "too many concurrent backtests — please retry shortly".into(),
                }),
            )
                .into_response();
        }
    };

    tracing::info!(symbol = %req.symbol, "dynamic backtest request");

    let base: BacktestRequest = req.into();
    let data_dir = Arc::clone(&state.data_dir);
    let result = tokio::task::spawn_blocking(move || runner::run(base, &data_dir)).await;
    backtest_response(result)
}

/// Compute indicators over historical data.
///
/// Returns a time series for each requested indicator, keyed by `label`
/// (auto-generated from type + params if not supplied).
/// Bars before indicator warmup are omitted from the series.
#[utoipa::path(
    post,
    path = "/api/indicator",
    tag = "data",
    security(("BearerAuth" = [])),
    request_body = IndicatorRequest,
    responses(
        (status = 200, description = "Indicator time series",                    body = IndicatorResponse),
        (status = 400, description = "Unknown indicator type or bad date range", body = ErrorResponse),
        (status = 429, description = "Too many concurrent requests",             body = ErrorResponse),
        (status = 500, description = "Internal error",                           body = ErrorResponse),
    )
)]
async fn compute_indicator(
    State(state): State<AppState>,
    Json(req): Json<IndicatorRequest>,
) -> Response {
    let _permit = match state.semaphore.try_acquire() {
        Ok(p) => p,
        Err(_) => {
            return (
                StatusCode::TOO_MANY_REQUESTS,
                Json(ErrorResponse { error: "too many concurrent requests — please retry shortly".into() }),
            ).into_response();
        }
    };

    tracing::info!(
        symbol     = %req.symbol,
        indicators = req.indicators.len(),
        from       = ?req.from,
        to         = ?req.to,
        "indicator request"
    );

    let data_dir = Arc::clone(&state.data_dir);
    let result = tokio::task::spawn_blocking(move || {
        runner::compute_indicators(req, &data_dir)
    }).await;

    match result {
        Ok(Ok(resp))  => (StatusCode::OK, Json(resp)).into_response(),
        Ok(Err(e)) => {
            tracing::warn!("indicator error: {:#}", e);
            (StatusCode::BAD_REQUEST, Json(ErrorResponse { error: e.to_string() })).into_response()
        }
        Err(e) => {
            tracing::error!("spawn_blocking panicked: {}", e);
            (StatusCode::INTERNAL_SERVER_ERROR, Json(ErrorResponse { error: "internal error".into() }))
                .into_response()
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn backtest_response(
    result: Result<anyhow::Result<BacktestResponse>, tokio::task::JoinError>,
) -> Response {
    match result {
        Ok(Ok(resp)) => (StatusCode::OK, Json(resp)).into_response(),
        Ok(Err(e)) => {
            tracing::warn!("backtest error: {:#}", e);
            (StatusCode::BAD_REQUEST, Json(ErrorResponse { error: e.to_string() })).into_response()
        }
        Err(e) => {
            tracing::error!("spawn_blocking panicked: {}", e);
            (StatusCode::INTERNAL_SERVER_ERROR, Json(ErrorResponse { error: "internal engine error".into() }))
                .into_response()
        }
    }
}

impl From<CelBacktestRequest> for BacktestRequest {
    fn from(req: CelBacktestRequest) -> Self {
        let mut params = serde_json::Map::new();
        params.insert("entry".into(), serde_json::Value::String(req.entry));
        params.insert("exit".into(), serde_json::Value::String(req.exit));
        if let Some(tp) = req.tp { params.insert("tp".into(), serde_json::json!(tp)); }
        if let Some(sl) = req.sl { params.insert("sl".into(), serde_json::json!(sl)); }
        BacktestRequest {
            strategy: "cel".into(),
            symbol: req.symbol,
            params: Some(serde_json::Value::Object(params)),
            exit: req.exit_config,
            from: req.from,
            to: req.to,
            initial_capital: req.initial_capital,
            commission_pct: req.commission_pct,
            slippage_pct: req.slippage_pct,
            risk_free_annual: req.risk_free_annual,
            position_size_pct: req.position_size_pct,
            market_hours_only: req.market_hours_only,
            exchange: req.exchange,
            asset_type: req.asset_type,
        }
    }
}

impl From<DynamicBacktestRequest> for BacktestRequest {
    fn from(req: DynamicBacktestRequest) -> Self {
        let mut params = serde_json::Map::new();
        params.insert("indicators".into(), serde_json::Value::Object(req.indicators));
        params.insert("entry".into(), req.entry);
        params.insert("exit".into(), req.exit);
        BacktestRequest {
            strategy: "dynamic".into(),
            symbol: req.symbol,
            params: Some(serde_json::Value::Object(params)),
            exit: req.exit_config,
            from: req.from,
            to: req.to,
            initial_capital: req.initial_capital,
            commission_pct: req.commission_pct,
            slippage_pct: req.slippage_pct,
            risk_free_annual: req.risk_free_annual,
            position_size_pct: req.position_size_pct,
            market_hours_only: req.market_hours_only,
            exchange: req.exchange,
            asset_type: req.asset_type,
        }
    }
}

/// Walk `data_dir` and return every parent-directory name that contains at
/// least one `.parquet` file.  Results are sorted and deduplicated.
fn discover_symbols(data_dir: &PathBuf) -> Vec<String> {
    let mut symbols: BTreeSet<String> = BTreeSet::new();
    for entry in WalkDir::new(data_dir)
        .follow_links(false)
        .into_iter()
        .filter_map(|e| e.ok())
    {
        let path = entry.path();
        if path.extension().and_then(|s| s.to_str()) == Some("parquet") {
            // Skip EOD directories
            let in_eod = path.components().any(|c| {
                c.as_os_str().to_str().map(|s| s.ends_with("_eod")).unwrap_or(false)
            });
            if in_eod {
                continue;
            }
            if let Some(parent) = path
                .parent()
                .and_then(|p| p.file_name())
                .and_then(|n| n.to_str())
            {
                symbols.insert(parent.to_uppercase());
            }
        }
    }
    symbols.into_iter().collect()
}
