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
    BacktestRequest, BacktestResponse, BarRecord, CelBacktestRequest, CurvePoint,
    DataQuery, DataResponse, DynamicBacktestRequest, ErrorResponse, ExitConfig,
    IndicatorConfig, IndicatorRequest, IndicatorResponse, LatestQuery, TradeResponse,
};
use crate::{backtest, indicator};

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
        CurvePoint,
        ErrorResponse,
        BarRecord,
        DataResponse,
        IndicatorRequest,
        IndicatorConfig,
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
        .route("/api/data/{symbol}",         get(get_data))
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

    let symbol_clone = symbol.clone();
    let result = tokio::task::spawn_blocking(move || {
        let files = find_parquet_files(&data_dir, &symbol_clone);
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
        ("n" = Option<usize>, Query, description = "Number of bars (default 500, max 5000)"),
        ("market_hours_only" = Option<bool>, Query, description = "Filter to regular market hours"),
        ("exchange" = Option<String>, Query, description = "`\"us\"` or `\"vn\"`"),
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
    let symbol_clone = symbol.clone();

    let result = tokio::task::spawn_blocking(move || {
        let files = find_parquet_files(&data_dir, &symbol_clone);
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
                    "exit": { "stop_loss_pct": 0.05, "trailing_stop_pct": 0.03 },
                    "initial_capital": 10000.0, "commission_pct": 0.001
                })
            )),
            ("MACD — AAPL" = (
                summary = "MACD crossover on AAPL 1min (2024–2026)",
                value = json!({
                    "strategy": "macd_crossover", "symbol": "AAPL",
                    "from": "2024-04-01", "to": "2026-01-01",
                    "params": { "fast": 12, "slow": 26, "signal": 9 },
                    "market_hours_only": true, "exchange": "us",
                    "commission_pct": 0.0, "initial_capital": 10000.0
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
                summary = "RSI oversold + price above EMA200 on BTCUSDT 1m",
                value = json!({
                    "symbol": "BTCUSDT",
                    "entry": "rsi(14) < 30 && close > ema(200)",
                    "exit": "rsi(14) > 70",
                    "sl": 0.05, "tp": 0.15,
                    "from": "2022-06-01", "to": "2024-01-01",
                    "initial_capital": 10000.0, "commission_pct": 0.001
                })
            )),
            ("EMA crossover — AAPL" = (
                summary = "EMA 9/21 crossover signal on AAPL 1min",
                value = json!({
                    "symbol": "AAPL",
                    "entry": "prev_ema(9) < prev_ema(21) && ema(9) > ema(21)",
                    "exit": "prev_ema(9) > prev_ema(21) && ema(9) < ema(21)",
                    "sl": 0.04,
                    "from": "2024-04-01", "to": "2026-01-01",
                    "market_hours_only": true, "exchange": "us",
                    "commission_pct": 0.0, "initial_capital": 10000.0,
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
                    "from": "2024-01-01", "to": "2026-01-01",
                    "market_hours_only": true, "exchange": "vn",
                    "commission_pct": 0.0015, "initial_capital": 100000000.0
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
                    "exit_config": { "stop_loss_pct": 0.05, "trailing_stop_pct": 0.03 }
                })
            )),
            ("MACD + RSI — AAPL" = (
                summary = "MACD histogram cross với RSI confirmation trên AAPL 1min",
                value = json!({
                    "symbol": "AAPL",
                    "from": "2024-04-01", "to": "2026-01-01",
                    "initial_capital": 10000.0, "commission_pct": 0.0,
                    "market_hours_only": true, "exchange": "us",
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

/// Compute indicators over historical data.
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
        params.insert("entry".into(), serde_json::Value::String(req.entry));
        params.insert("exit".into(),  serde_json::Value::String(req.exit));
        if let Some(tp) = req.tp { params.insert("tp".into(), json!(tp)); }
        if let Some(sl) = req.sl { params.insert("sl".into(), json!(sl)); }
        BacktestRequest {
            strategy: "cel".into(),
            symbol: req.symbol,
            params: Some(serde_json::Value::Object(params)),
            exit: req.exit_config,
            from: req.from, to: req.to,
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
        params.insert("exit".into(),  req.exit);
        BacktestRequest {
            strategy: "dynamic".into(),
            symbol: req.symbol,
            params: Some(serde_json::Value::Object(params)),
            exit: req.exit_config,
            from: req.from, to: req.to,
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
