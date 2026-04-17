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
use serde_json::json;
use tokio::sync::Semaphore;
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;
use utoipa::openapi::security::{HttpAuthScheme, HttpBuilder, SecurityScheme};
use utoipa::{Modify, OpenApi};
use utoipa_swagger_ui::SwaggerUi;
use walkdir::WalkDir;

use crate::catalog::{self, IndicatorMeta, ParamDef, STRATEGY_KEYS};
use crate::data::{find_parquet_files, load_bars, parse_date_ms};
use crate::types::{
    BacktestRequest, BacktestResponse, BarRecord, BuyHoldBenchmarkResponse, CandlesQuery,
    CandlesResult, CelBacktestRequest, CurvePoint, DataQuery, DataResponse,
    DynamicBacktestRequest, ErrorResponse, ExitConfig, ExitLevel, IndicatorConfig, IndicatorPoint,
    IndicatorRequest, IndicatorResponse, LatestQuery, RegimeSummaryResponse, TradeResponse,
    UnifiedDataRequest, UnifiedDataResponse,
};
use crate::{backtest, indicator};
use crate::indicator::run_indicators_on_bars;

// ── App state ─────────────────────────────────────────────────────────────────

#[derive(Clone)]
pub struct AppState {
    pub data_dir:  Arc<PathBuf>,
    /// Limits concurrent CPU-bound tasks to protect VPS RAM/CPU.
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
        list_indicators,
        get_data,
        get_latest,
        unified_data,
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
        ExitLevel,
        BacktestResponse,
        TradeResponse,
        CurvePoint,
        BuyHoldBenchmarkResponse,
        RegimeSummaryResponse,
        ErrorResponse,
        BarRecord,
        DataResponse,
        CandlesQuery,
        CandlesResult,
        UnifiedDataRequest,
        UnifiedDataResponse,
        IndicatorRequest,
        IndicatorConfig,
        IndicatorPoint,
        IndicatorResponse,
        IndicatorMeta,
        ParamDef,
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
        .route("/health",                    get(health))
        .route("/api/strategies",            get(list_strategies))
        .route("/api/symbols",               get(list_symbols))
        .route("/api/indicators",            get(list_indicators))
        .route("/api/data/{symbol}",         get(get_data).post(unified_data))
        .route("/api/data/{symbol}/latest",  get(get_latest))
        .route("/api/backtest",              post(run_backtest))
        .route("/api/backtest/cel",          post(run_backtest_cel))
        .route("/api/backtest/dynamic",      post(run_backtest_dynamic))
        .route("/api/indicator",             post(compute_indicator))
        .with_state(state);

    Router::new()
        .merge(SwaggerUi::new("/swagger/logbook").url("/api-doc/openapi.json", ApiDoc::openapi()))
        .merge(api)
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http())
}

// ── Handlers ──────────────────────────────────────────────────────────────────

/// Simple health check.
#[utoipa::path(get, path = "/health", tag = "meta",
    responses((status = 200, description = "Service is healthy", body = serde_json::Value))
)]
async fn health() -> impl IntoResponse {
    Json(json!({ "ok": true }))
}

/// List available strategy keys.
#[utoipa::path(get, path = "/api/strategies", tag = "meta",
    security(("BearerAuth" = [])),
    responses((status = 200, description = "List of strategy keys", body = Vec<String>))
)]
async fn list_strategies() -> impl IntoResponse {
    Json(STRATEGY_KEYS)
}

/// List all supported indicator types with their parameter schemas and output fields.
#[utoipa::path(get, path = "/api/indicators", tag = "meta",
    security(("BearerAuth" = [])),
    responses((status = 200, description = "Indicator catalog", body = Vec<IndicatorMeta>))
)]
async fn list_indicators() -> impl IntoResponse {
    Json(catalog::all())
}

/// List symbols that have data available in the data directory.
#[utoipa::path(get, path = "/api/symbols", tag = "data",
    security(("BearerAuth" = [])),
    responses((status = 200, description = "Sorted list of available symbols", body = Vec<String>))
)]
async fn list_symbols(State(state): State<AppState>) -> impl IntoResponse {
    let data_dir = Arc::clone(&state.data_dir);
    let symbols = tokio::task::spawn_blocking(move || discover_symbols(&data_dir))
        .await
        .unwrap_or_default();
    Json(symbols)
}

/// Load OHLCV bars for a symbol.
#[utoipa::path(
    get, path = "/api/data/{symbol}", tag = "data",
    security(("BearerAuth" = [])),
    params(
        ("symbol" = String, Path, description = "Asset symbol, e.g. `BTCUSDT` or `NVDA`"),
        DataQuery,
    ),
    responses(
        (status = 200, description = "OHLCV bars",                         body = DataResponse),
        (status = 400, description = "Symbol not found or bad date range", body = ErrorResponse),
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
        parse_date_ms(s).map(|ms| ms + 86_400_000 - 1)
    });

    let timeframe = q.timeframe.clone();
    let symbol_clone = symbol.clone();
    let result = tokio::task::spawn_blocking(move || {
        let files = find_parquet_files(&data_dir, &symbol_clone, timeframe.as_deref());
        load_bars(&files, &symbol_clone, from_ms, to_ms, market_hours_only, &exchange)
    }).await;

    match result {
        Ok(Ok(mut feed)) => {
            use alm_data::BarFeed;
            let mut bars: Vec<BarRecord> = Vec::with_capacity(limit.min(feed.len()));
            while let Some(bar) = feed.next() {
                bars.push(BarRecord {
                    t: bar.timestamp, o: bar.open, h: bar.high,
                    l: bar.low,       c: bar.close, v: bar.volume,
                    vwap: bar.vwap, n: bar.transactions,
                });
                if bars.len() >= limit { break; }
            }
            let count = bars.len();
            (StatusCode::OK, Json(DataResponse { symbol, count, bars })).into_response()
        }
        Ok(Err(e)) => {
            tracing::warn!("data load error for {symbol}: {:#}", e);
            (StatusCode::BAD_REQUEST, Json(ErrorResponse { error: e.to_string() })).into_response()
        }
        Err(e) => {
            tracing::error!("spawn_blocking panicked: {}", e);
            (StatusCode::INTERNAL_SERVER_ERROR, Json(ErrorResponse { error: "internal error".into() }))
                .into_response()
        }
    }
}

/// Return the last N bars for a symbol (default 500, max 5 000).
#[utoipa::path(
    get, path = "/api/data/{symbol}/latest", tag = "data",
    security(("BearerAuth" = [])),
    params(
        ("symbol" = String, Path, description = "Asset symbol, e.g. `BTCUSDT`"),
        LatestQuery,
    ),
    responses(
        (status = 200, description = "Latest N OHLCV bars", body = DataResponse),
        (status = 400, description = "Symbol not found",    body = ErrorResponse),
    )
)]
async fn get_latest(
    State(state): State<AppState>,
    Path(symbol): Path<String>,
    Query(q): Query<LatestQuery>,
) -> Response {
    let data_dir = Arc::clone(&state.data_dir);
    let limit = q.n.unwrap_or(500).min(5_000).max(1);
    let market_hours_only = q.market_hours_only.unwrap_or(false);
    let exchange = q.exchange.clone().unwrap_or_else(|| "us".to_string());
    let timeframe = q.timeframe.clone();
    let symbol_clone = symbol.clone();

    let result = tokio::task::spawn_blocking(move || {
        let files = find_parquet_files(&data_dir, &symbol_clone, timeframe.as_deref());
        load_bars(&files, &symbol_clone, None, None, market_hours_only, &exchange)
    }).await;

    match result {
        Ok(Ok(mut feed)) => {
            use alm_data::BarFeed;
            let mut all: Vec<BarRecord> = Vec::with_capacity(feed.len().min(limit * 2));
            while let Some(bar) = feed.next() {
                all.push(BarRecord {
                    t: bar.timestamp, o: bar.open, h: bar.high,
                    l: bar.low,       c: bar.close, v: bar.volume,
                    vwap: bar.vwap, n: bar.transactions,
                });
            }
            let start = all.len().saturating_sub(limit);
            let bars: Vec<BarRecord> = all.into_iter().skip(start).collect();
            let count = bars.len();
            (StatusCode::OK, Json(DataResponse { symbol, count, bars })).into_response()
        }
        Ok(Err(e)) => (
            StatusCode::BAD_REQUEST,
            Json(ErrorResponse { error: e.to_string() }),
        ).into_response(),
        Err(e) => {
            tracing::error!("spawn_blocking panicked: {}", e);
            (StatusCode::INTERNAL_SERVER_ERROR, Json(ErrorResponse { error: "internal error".into() }))
                .into_response()
        }
    }
}

/// Run a backtest with a named strategy.
#[utoipa::path(
    post, path = "/api/backtest", tag = "backtest",
    security(("BearerAuth" = [])),
    request_body(
        content = BacktestRequest,
        examples(
            ("RSI — BTCUSDT" = (
                summary = "RSI mean reversion on BTCUSDT 1m (2022–2024)",
                value = json!({
                    "strategy": "rsi_mean_rev", "symbol": "BTCUSDT",
                    "from": "2022-06-01", "to": "2024-01-01",
                    "params": { "period": 14, "oversold": 30, "overbought": 70 },
                    "exit": { "stop_loss_pct": 0.05 },
                    "initial_capital": 10000.0, "commission_pct": 0.001
                })
            )),
            ("MACD — BNBUSDT" = (
                summary = "MACD crossover on BNBUSDT 1min (2024–2026)",
                value = json!({
                    "strategy": "macd_crossover", "symbol": "BNBUSDT",
                    "from": "2024-04-01", "to": "2026-01-01",
                    "params": { "fast": 12, "slow": 26, "signal": 9 },
                    "commission_pct": 0.001, "initial_capital": 10000.0,
                    "asset_type": "crypto", "timeframe": "M1"
                })
            )),
            ("EMA crossover — FPT" = (
                summary = "EMA 9/21 crossover on FPT (VN stock)",
                value = json!({
                    "strategy": "ma_crossover", "symbol": "FPT",
                    "from": "2024-01-01", "to": "2026-01-01",
                    "params": { "fast": 9, "slow": 21 },
                    "market_hours_only": true, "exchange": "vn",
                    "commission_pct": 0.0015, "initial_capital": 100000000.0
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
        Err(_) => return too_many_requests(),
    };
    tracing::info!(strategy = %req.strategy, symbol = %req.symbol, "HTTP backtest request");
    let data_dir = Arc::clone(&state.data_dir);
    let result = tokio::task::spawn_blocking(move || backtest::run(req, &data_dir)).await;
    backtest_response(result)
}

/// Run a backtest with a CEL expression strategy.
#[utoipa::path(
    post, path = "/api/backtest/cel", tag = "backtest",
    security(("BearerAuth" = [])),
    request_body(
        content = CelBacktestRequest,
        examples(
            ("RSI + EMA — BTCUSDT" = (
                summary = "RSI oversold + EMA200 filter, ATR-based tp/sl, peak trailing",
                value = json!({
                    "symbol": "BTCUSDT",
                    "entry_expr": "rsi(14) < 30 && close > ema(200)",
                    "exit_expr":  "rsi(14) > 70 || close < peak - 2.0*atr(14)",
                    "exit": { "tp": "3*atr(14)", "sl": "1.5*atr(14)", "max_bars": 200 },
                    "position_size_pct": 0.95, "max_positions": 1,
                    "from": "2022-06-01", "to": "2024-01-01",
                    "initial_capital": 10000.0, "commission_pct": 0.001
                })
            )),
            ("EMA crossover — ETHUSDT" = (
                summary = "EMA 9/21 crossover on ETHUSDT 1min with Heiken Ashi",
                value = json!({
                    "symbol": "ETHUSDT",
                    "entry_expr": "prev_ema(9) < prev_ema(21) && ema(9) > ema(21)",
                    "exit_expr":  "prev_ema(9) > prev_ema(21) && ema(9) < ema(21)",
                    "exit": { "sl": 0.04 },
                    "candle_type": "heiken_ashi",
                    "position_size_pct": 0.95, "max_positions": 1,
                    "from": "2024-04-01", "to": "2026-01-01",
                    "commission_pct": 0.001, "initial_capital": 10000.0,
                    "asset_type": "crypto", "timeframe": "M1"
                })
            )),
            ("MACD histogram — FPT" = (
                summary = "MACD zero-cross on FPT (VN stock), fixed $-amount sizing",
                value = json!({
                    "symbol": "FPT",
                    "entry_expr": "prev_macd_hist(12) < 0 && macd_hist(12) > 0 && adx(14) > 20",
                    "exit_expr":  "prev_macd_hist(12) > 0 && macd_hist(12) < 0",
                    "exit": { "sl": 0.05, "tp": 0.15 },
                    "position_size_usd": 10000000.0, "max_positions": 3,
                    "from": "2024-01-01", "to": "2026-01-01",
                    "market_hours_only": true, "exchange": "vn",
                    "commission_pct": 0.0015, "initial_capital": 100000000.0,
                    "asset_type": "vn_stock"
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
        Err(_) => return too_many_requests(),
    };
    tracing::info!(symbol = %req.symbol, "CEL backtest request");
    let base: BacktestRequest = req.into();
    let data_dir = Arc::clone(&state.data_dir);
    let result = tokio::task::spawn_blocking(move || backtest::run(base, &data_dir)).await;
    backtest_response(result)
}

/// Run a backtest with a JSON-defined (dynamic) strategy.
#[utoipa::path(
    post, path = "/api/backtest/dynamic", tag = "backtest",
    security(("BearerAuth" = [])),
    request_body(
        content = DynamicBacktestRequest,
        examples(
            ("RSI + EMA — BTCUSDT" = (
                summary = "RSI oversold + price above EMA200 on BTCUSDT 1m",
                value = json!({
                    "symbol": "BTCUSDT",
                    "from": "2022-06-01", "to": "2024-01-01",
                    "initial_capital": 10000.0, "commission_pct": 0.001,
                    "indicators": {
                        "rsi14":  { "type": "rsi", "period": 14 },
                        "ema200": { "type": "ema", "period": 200 }
                    },
                    "entry": { "logic": "and", "rules": [
                        { "source": "rsi14", "field": "value", "op": "lt", "value": 30 },
                        { "source": "close", "field": "value", "op": "gt", "compare": "ema200" }
                    ]},
                    "exit": { "logic": "or", "rules": [
                        { "source": "rsi14", "field": "value", "op": "gt", "value": 70 }
                    ]},
                    "exit_config": { "stop_loss_pct": 0.05 }
                })
            )),
            ("MACD + RSI — BNBUSDT" = (
                summary = "MACD histogram cross với RSI confirmation trên BNBUSDT 1min",
                value = json!({
                    "symbol": "BNBUSDT",
                    "from": "2024-04-01", "to": "2026-01-01",
                    "initial_capital": 10000.0, "commission_pct": 0.001,
                    "asset_type": "crypto", "timeframe": "M1",
                    "indicators": {
                        "macd":  { "type": "macd", "fast": 12, "slow": 26, "signal": 9 },
                        "rsi14": { "type": "rsi",  "period": 14 }
                    },
                    "entry": { "logic": "and", "rules": [
                        { "source": "macd",  "field": "histogram", "op": "cross_above", "value": 0 },
                        { "source": "rsi14", "field": "value",     "op": "lt",          "value": 60 }
                    ]},
                    "exit": { "logic": "or", "rules": [
                        { "source": "macd",  "field": "histogram", "op": "cross_below", "value": 0 },
                        { "source": "rsi14", "field": "value",     "op": "gt",          "value": 75 }
                    ]}
                })
            )),
            ("BB + RSI — FPT" = (
                summary = "Bollinger Band squeeze với RSI trên FPT (VN)",
                value = json!({
                    "symbol": "FPT",
                    "from": "2024-01-01", "to": "2026-01-01",
                    "initial_capital": 100000000.0, "commission_pct": 0.0015,
                    "market_hours_only": true, "exchange": "vn",
                    "indicators": {
                        "rsi14": { "type": "rsi",    "period": 14 },
                        "bb20":  { "type": "bbands", "period": 20, "std_dev": 2.0 }
                    },
                    "entry": { "logic": "and", "rules": [
                        { "source": "rsi14", "field": "value", "op": "lt", "value": 35 },
                        { "source": "close", "field": "value", "op": "lt", "compare": "bb20.lower" }
                    ]},
                    "exit": { "logic": "or", "rules": [
                        { "source": "rsi14", "field": "value", "op": "gt", "value": 65 },
                        { "source": "close", "field": "value", "op": "gt", "compare": "bb20.upper" }
                    ]},
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
        Err(_) => return too_many_requests(),
    };
    tracing::info!(symbol = %req.symbol, "dynamic backtest request");
    let base: BacktestRequest = req.into();
    let data_dir = Arc::clone(&state.data_dir);
    let result = tokio::task::spawn_blocking(move || backtest::run(base, &data_dir)).await;
    backtest_response(result)
}

/// Unified OHLCV + indicator query for a symbol.
///
/// Two independent sections control what is returned:
/// - `candles` present → include OHLCV bars; `candles.limit` without `from`/`to` = last N bars
/// - `indicators` present and non-empty → compute and include indicator series
/// - both present → bars + series in one round-trip (chart init)
/// - only `indicators` → series only (caller already has bars)
#[utoipa::path(
    post, path = "/api/data/{symbol}", tag = "data",
    security(("BearerAuth" = [])),
    params(("symbol" = String, Path, description = "Asset symbol, e.g. `BTCUSDT` or `NVDA`")),
    request_body(
        content = UnifiedDataRequest,
        examples(
            ("Candles + EMA/RSI — BTCUSDT" = (
                summary = "H1 Heiken Ashi bars + EMA20 + RSI14 in one round-trip",
                value = json!({
                    "from": "2024-01-01", "to": "2024-06-30",
                    "timeframe": "H1",
                    "candles": { "candle_type": "heiken_ashi" },
                    "indicators": [
                        { "type": "ema", "period": 20, "label": "ema20" },
                        { "type": "rsi", "period": 14, "label": "rsi14" }
                    ]
                })
            )),
            ("Indicator-only — ETHUSDT" = (
                summary = "Only MACD series, caller already has candles",
                value = json!({
                    "from": "2024-01-01", "to": "2024-06-30",
                    "timeframe": "H1",
                    "indicators": [
                        { "type": "macd", "fast": 12, "slow": 26, "signal": 9 }
                    ]
                })
            )),
            ("Latest 500 bars — FPT" = (
                summary = "Last 500 VN market-hours bars, no indicators",
                value = json!({
                    "market_hours_only": true, "exchange": "vn",
                    "candles": { "limit": 500 }
                })
            ))
        )
    ),
    responses(
        (status = 200, description = "Bars and/or indicator series",             body = UnifiedDataResponse),
        (status = 400, description = "Symbol not found or bad request",          body = ErrorResponse),
        (status = 429, description = "Too many concurrent requests",             body = ErrorResponse),
        (status = 500, description = "Internal error",                           body = ErrorResponse),
    )
)]
async fn unified_data(
    State(state): State<AppState>,
    Path(symbol): Path<String>,
    Json(req): Json<UnifiedDataRequest>,
) -> Response {
    let _permit = match state.semaphore.try_acquire() {
        Ok(p) => p,
        Err(_) => return too_many_requests(),
    };
    tracing::info!(symbol = %symbol, "unified data request");

    let data_dir  = Arc::clone(&state.data_dir);
    let from_ms   = req.from.as_deref().and_then(parse_date_ms);
    let to_ms     = req.to.as_deref().and_then(|s| {
        parse_date_ms(s).map(|ms| ms + 86_400_000 - 1)
    });
    let market_hours_only = req.market_hours_only.unwrap_or(false);
    let exchange          = req.exchange.clone().unwrap_or_else(|| "us".to_string());
    let timeframe         = req.timeframe.clone();

    let candles_query   = req.candles;
    let want_candles    = candles_query.is_some();
    let limit           = candles_query.as_ref().and_then(|c| c.limit);
    let latest_mode     = from_ms.is_none() && to_ms.is_none() && limit.is_some();
    let candle_type_str = candles_query.as_ref().and_then(|c| c.candle_type.clone());
    let ha_smooth       = candles_query.as_ref().and_then(|c| c.ha_smooth).unwrap_or(2);

    let ind_configs     = req.indicators.unwrap_or_default();
    let want_indicators = !ind_configs.is_empty();

    let result = tokio::task::spawn_blocking(move || {
        use alm_data::BarFeed;
        use alm_strategy::candle_type::{CandleTransform, CandleType};

        let files = find_parquet_files(&data_dir, &symbol, timeframe.as_deref());
        let mut feed = load_bars(&files, &symbol, from_ms, to_ms, market_hours_only, &exchange)?;

        let total = feed.len();
        let mut raw_bars: Vec<alm_core::Bar> = Vec::with_capacity(total);
        while let Some(b) = feed.next() {
            raw_bars.push(b);
        }

        // Apply candle transform (raw / heiken_ashi / smooth_ha).
        // Both indicator series and returned bars reflect the same transform.
        let transformed: Vec<alm_core::Bar> = if let Some(ref ct) = candle_type_str {
            let kind = CandleType::from_str(ct, ha_smooth);
            let mut xform = CandleTransform::new(kind);
            raw_bars.iter().filter_map(|b| xform.apply(b)).collect()
        } else {
            raw_bars
        };

        let bars_slice: &[alm_core::Bar] = if let Some(n) = limit {
            if latest_mode {
                let start = transformed.len().saturating_sub(n);
                &transformed[start..]
            } else {
                &transformed[..n.min(transformed.len())]
            }
        } else {
            &transformed
        };

        let indicators_out = if want_indicators {
            Some(run_indicators_on_bars(bars_slice, &ind_configs)?)
        } else {
            None
        };

        let candles_out = if want_candles {
            let bars = bars_slice.iter().map(|b| BarRecord {
                t: b.timestamp, o: b.open, h: b.high,
                l: b.low,       c: b.close, v: b.volume,
                vwap: b.vwap, n: b.transactions,
            }).collect::<Vec<_>>();
            Some(CandlesResult { count: bars.len(), bars })
        } else {
            None
        };

        anyhow::Ok(UnifiedDataResponse { symbol, candles: candles_out, indicators: indicators_out })
    }).await;

    match result {
        Ok(Ok(resp))  => (StatusCode::OK, Json(resp)).into_response(),
        Ok(Err(e)) => {
            tracing::warn!("unified data error: {:#}", e);
            (StatusCode::BAD_REQUEST, Json(ErrorResponse { error: e.to_string() })).into_response()
        }
        Err(e) => {
            tracing::error!("spawn_blocking panicked: {}", e);
            (StatusCode::INTERNAL_SERVER_ERROR, Json(ErrorResponse { error: "internal error".into() }))
                .into_response()
        }
    }
}

/// ⚠ Deprecated — use `POST /api/data/{symbol}` instead.
#[utoipa::path(
    post, path = "/api/indicator", tag = "data",
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
        Err(_) => return too_many_requests(),
    };
    tracing::info!(
        symbol     = %req.symbol,
        indicators = req.indicators.len(),
        "indicator request"
    );
    let data_dir = Arc::clone(&state.data_dir);
    let result = tokio::task::spawn_blocking(move || {
        indicator::compute_indicators(req, &data_dir)
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

// ── Request conversions ───────────────────────────────────────────────────────

impl From<CelBacktestRequest> for BacktestRequest {
    fn from(req: CelBacktestRequest) -> Self {
        let mut params = serde_json::Map::new();
        params.insert("entry".into(), json!(req.entry_expr));
        params.insert("exit".into(),  json!(req.exit_expr));
        if let Some(v) = req.candle_type { params.insert("candle_type".into(), json!(v)); }
        if let Some(v) = req.ha_smooth   { params.insert("ha_smooth".into(),   json!(v)); }
        // ATR-based tp/sl/trail from ExitConfig → injected into CEL params
        if let Some(ref cfg) = req.exit {
            crate::backtest::inject_atr_exit_into_cel_params(cfg, &mut params);
        }
        BacktestRequest {
            strategy: "cel".into(),
            symbol: req.symbol,
            params: Some(serde_json::Value::Object(params)),
            exit: req.exit,
            from: req.from, to: req.to,
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
        params.insert("indicators".into(), serde_json::Value::Object(req.indicators));
        params.insert("entry".into(), req.entry);
        params.insert("exit".into(),  req.exit);
        BacktestRequest {
            strategy: "dynamic".into(),
            symbol: req.symbol,
            params: Some(serde_json::Value::Object(params)),
            exit: req.exit_rules,
            from: req.from, to: req.to,
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

// ── Private helpers ───────────────────────────────────────────────────────────

fn too_many_requests() -> Response {
    (
        StatusCode::TOO_MANY_REQUESTS,
        Json(ErrorResponse { error: "too many concurrent requests — please retry shortly".into() }),
    ).into_response()
}

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

fn discover_symbols(data_dir: &PathBuf) -> Vec<String> {
    let mut symbols: BTreeSet<String> = BTreeSet::new();
    for entry in WalkDir::new(data_dir).follow_links(false).into_iter().filter_map(|e| e.ok()) {
        let path = entry.path();
        if path.extension().and_then(|s| s.to_str()) == Some("parquet") {
            let in_eod = path.components().any(|c| {
                c.as_os_str().to_str().map(|s| s.ends_with("_eod")).unwrap_or(false)
            });
            if in_eod { continue; }
            if let Some(parent) = path.parent()
                .and_then(|p| p.file_name())
                .and_then(|n| n.to_str())
            {
                symbols.insert(parent.to_uppercase());
            }
        }
    }
    symbols.into_iter().collect()
}
