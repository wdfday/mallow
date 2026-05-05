//! Backtest endpoints — thin adapters over [`alm_engine::backtest::run`].
//!
//! The heavy lifting (Parquet loading, engine loop, report aggregation) lives
//! in `alm-engine` as a plain library function. Herald's job here is limited to:
//!
//! - Serialising the request / response envelopes.
//! - Enforcing a concurrency cap via [`HttpState::backtest_semaphore`].
//! - Dispatching the CPU-bound engine run onto `spawn_blocking`.
//! - Optionally auto-saving the result to the store when `save_as` is set.
//!
//! # save_as
//!
//! `POST /api/backtest/cel` and `POST /api/backtest/rhai` accept an optional
//! `"save_as"` field. When present, a successful run automatically creates a
//! strategy version + backtest case + result row in the store and returns their
//! IDs under `"saved"` in the response. Omit the field for throwaway runs.
//!
//! # Concurrency
//!
//! The semaphore is acquired with `try_acquire`; at-capacity requests receive
//! `429 Too Many Requests` instead of queueing. This keeps tail latency
//! bounded — the client can retry with back-off.

use std::sync::Arc;

use anyhow::Result;
use axum::{
    extract::State,
    http::StatusCode,
    response::{IntoResponse, Response},
    Json, Router,
};
use alm_engine::{
    backtest,
    types::{BacktestRequest, BacktestResponse, CelBacktestRequest, ErrorResponse, RhaiBacktestRequest},
};
use alm_strategy::catalog::STRATEGY_KEYS;
use chrono::{NaiveDate, TimeZone, Utc};
use serde::Serialize;
use tracing::{error, info, warn};

use axum::routing::{get, post};

use super::store::types::{CapitalConfig, ExecutionConfig, StrategySpec};
use super::store::StoreBackend;
use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/strategies", get(list_strategies))
        .route("/api/v1/backtest", post(run_backtest))
        .route("/api/v1/backtest/estimate", post(estimate_backtest))
        .route("/api/v1/backtest/cel", post(run_backtest_cel))
        .route("/api/v1/backtest/rhai", post(run_backtest_rhai))
}

// ── Estimate response ─────────────────────────────────────────────────────────

#[derive(Debug, Serialize)]
pub struct EstimateResponse {
    pub bar_count: usize,
    pub estimated_seconds: f64,
    pub within_limit: bool,
    pub limit: usize,
}

// ── GET /api/strategies ──────────────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/v1/strategies",
    responses(
        (status = 200, description = "List of registered named strategy keys")
    ),
    tag = "backtest"
)]
pub async fn list_strategies() -> Json<&'static [&'static str]> {
    Json(STRATEGY_KEYS)
}

// ── POST /api/backtest/estimate ──────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest/estimate",
    responses(
        (status = 200, description = "Bar count + estimated run time without running the engine"),
        (status = 400, description = "Bad request")
    ),
    tag = "backtest"
)]
pub async fn estimate_backtest(
    State(state): State<HttpState>,
    Json(req): Json<BacktestRequest>,
) -> Response {
    const MAX_BARS: usize = 100_000;
    let data_dir = Arc::clone(&state.data_dir);
    match tokio::task::spawn_blocking(move || backtest::estimate(&req, &data_dir)).await {
        Ok(Ok((bar_count, estimated_seconds))) => {
            (StatusCode::OK, Json(EstimateResponse {
                bar_count,
                estimated_seconds,
                within_limit: bar_count <= MAX_BARS,
                limit: MAX_BARS,
            })).into_response()
        }
        Ok(Err(e)) => {
            warn!(error = %e, "estimate failed");
            (StatusCode::BAD_REQUEST, Json(ErrorResponse { error: e.to_string() })).into_response()
        }
        Err(e) => {
            error!(error = %e, "estimate spawn_blocking panicked");
            (StatusCode::INTERNAL_SERVER_ERROR, Json(ErrorResponse { error: "internal error".into() })).into_response()
        }
    }
}

// ── POST /api/backtest ───────────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest",
    responses(
        (status = 200, description = "Backtest report"),
        (status = 400, description = "Bad request"),
        (status = 429, description = "Too many concurrent backtests")
    ),
    tag = "backtest"
)]
pub async fn run_backtest(
    State(state): State<HttpState>,
    Json(req): Json<BacktestRequest>,
) -> Response {
    let Some(_permit) = try_acquire(&state) else {
        return too_many_requests();
    };
    info!(strategy = %req.strategy, symbol = %req.symbol, "HTTP backtest request");
    match run_blocking(Arc::clone(&state.data_dir), req).await {
        Ok(report) => (StatusCode::OK, Json(report)).into_response(),
        Err(r) => r,
    }
}

// ── POST /api/backtest/cel ───────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest/cel",
    responses(
        (status = 200, description = "CEL backtest report"),
        (status = 400, description = "Bad request"),
        (status = 429, description = "Too many concurrent backtests")
    ),
    tag = "backtest"
)]
pub async fn run_backtest_cel(
    State(state): State<HttpState>,
    Json(req): Json<CelBacktestRequest>,
) -> Response {
    let Some(_permit) = try_acquire(&state) else {
        return too_many_requests();
    };
    let save_as = req.save_as.clone();
    let spec = StrategySpec::Cel {
        entry: req.entry_expr.clone(),
        exit: req.exit_expr.clone(),
        defines: req.defines.clone(),
    };
    let symbol = req.symbol.clone();
    let timeframe = req.timeframe.clone();
    let from = req.from.clone();
    let to = req.to.clone();
    let capital = capital_from_cel(&req);
    let execution = execution_from_cel(&req);
    let exit = req.exit.clone();
    let data_source = req.data_source.clone();

    info!(symbol = %req.symbol, ?save_as, "CEL backtest request");
    let base: BacktestRequest = req.into();
    match run_blocking(Arc::clone(&state.data_dir), base).await {
        Ok(report) => {
            respond_with_save(
                report, save_as, spec,
                symbol, timeframe, from, to,
                capital, execution, exit, data_source,
                &state.store,
            ).await
        }
        Err(r) => r,
    }
}

// ── POST /api/backtest/rhai ──────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest/rhai",
    responses(
        (status = 200, description = "Rhai backtest report"),
        (status = 400, description = "Bad request"),
        (status = 429, description = "Too many concurrent backtests")
    ),
    tag = "backtest"
)]
pub async fn run_backtest_rhai(
    State(state): State<HttpState>,
    Json(req): Json<RhaiBacktestRequest>,
) -> Response {
    let Some(_permit) = try_acquire(&state) else {
        return too_many_requests();
    };
    let save_as = req.save_as.clone();
    let spec = StrategySpec::Rhai { script: req.script.clone() };
    let symbol = req.symbol.clone();
    let timeframe = req.timeframe.clone();
    let from = req.from.clone();
    let to = req.to.clone();
    let capital = capital_from_rhai(&req);
    let execution = execution_from_rhai(&req);
    let exit = req.exit.clone();
    let data_source = req.data_source.clone();

    info!(symbol = %req.symbol, ?save_as, "Rhai backtest request");
    let base: BacktestRequest = req.into();
    match run_blocking(Arc::clone(&state.data_dir), base).await {
        Ok(report) => {
            respond_with_save(
                report, save_as, spec,
                symbol, timeframe, from, to,
                capital, execution, exit, data_source,
                &state.store,
            ).await
        }
        Err(r) => r,
    }
}

// ── Core helpers ─────────────────────────────────────────────────────────────

fn try_acquire(state: &HttpState) -> Option<tokio::sync::OwnedSemaphorePermit> {
    Arc::clone(&state.backtest_semaphore).try_acquire_owned().ok()
}

/// Run the engine on a blocking thread. Returns `Ok(report)` or `Err(error_response)`.
pub(super) async fn run_blocking(
    data_dir: Arc<std::path::PathBuf>,
    req: BacktestRequest,
) -> Result<BacktestResponse, Response> {
    match tokio::task::spawn_blocking(move || backtest::run(req, &data_dir)).await {
        Ok(Ok(resp)) => Ok(resp),
        Ok(Err(e)) => {
            warn!(error = %e, "backtest failed");
            Err((StatusCode::BAD_REQUEST, Json(ErrorResponse { error: e.to_string() })).into_response())
        }
        Err(e) => {
            error!(error = %e, "backtest spawn_blocking panicked");
            Err((StatusCode::INTERNAL_SERVER_ERROR, Json(ErrorResponse { error: "internal engine error".into() })).into_response())
        }
    }
}

/// Serialize the report and, if `save_as` is set, auto-save and merge `"saved"` into the JSON.
#[allow(clippy::too_many_arguments)]
async fn respond_with_save(
    report: BacktestResponse,
    save_as: Option<String>,
    spec: StrategySpec,
    symbol: String,
    timeframe: Option<String>,
    from: Option<String>,
    to: Option<String>,
    capital: CapitalConfig,
    execution: ExecutionConfig,
    exit: Option<alm_engine::types::ExitConfig>,
    data_source: Option<String>,
    store: &StoreBackend,
) -> Response {
    let Some(name) = save_as else {
        return (StatusCode::OK, Json(report)).into_response();
    };

    let saved = auto_save(
        store, name, spec,
        symbol, timeframe,
        from.as_deref(), to.as_deref(),
        capital, execution, exit, data_source,
        &report,
    ).await;

    // Merge `"saved"` into the report JSON — avoids touching BacktestResponse type.
    let mut body = serde_json::to_value(&report).unwrap_or_default();
    match saved {
        Ok(r) => {
            body["saved"] = serde_json::to_value(&r).unwrap_or_default();
            info!(strategy_id = %r.strategy_id, result_id = %r.result_id, "auto-saved backtest");
        }
        Err(e) => {
            warn!(error = %e, "auto-save failed — result still returned");
            body["saved_error"] = serde_json::Value::String(e.to_string());
        }
    }
    (StatusCode::OK, Json(body)).into_response()
}

/// IDs returned when `save_as` triggers auto-save.
#[derive(Debug, Serialize)]
pub struct SavedRef {
    pub strategy_id: String,
    pub case_id: String,
    pub result_id: String,
}

async fn auto_save(
    store: &StoreBackend,
    name: String,
    spec: StrategySpec,
    symbol: String,
    timeframe: Option<String>,
    from: Option<&str>,
    to: Option<&str>,
    capital: CapitalConfig,
    execution: ExecutionConfig,
    exit: Option<alm_engine::types::ExitConfig>,
    data_source: Option<String>,
    report: &BacktestResponse,
) -> Result<SavedRef> {
    let label = name.clone();
    let strategy = store.create_strategy(name.clone(), None, label, spec, None).await?;

    let case = store.create_case(
        strategy.id.clone(),
        format!("{name} on {symbol}"),
        symbol,
        timeframe,
        from.and_then(date_to_ms),
        to.and_then(date_to_ms),
        data_source,
        capital,
        execution,
        exit,
    ).await?;

    let ran_at = Utc::now().timestamp_millis();
    let result = store.save_result(
        case.id.clone(),
        ran_at,
        None,
        report.returns.total_pct,
        report.risk_adjusted.sharpe,
        report.drawdown.max_pct,
        report.trade_stats.win_rate_pct,
        report.trade_stats.total as i64,
    ).await?;

    Ok(SavedRef { strategy_id: strategy.id, case_id: case.id, result_id: result.id })
}

// ── Request field extractors ──────────────────────────────────────────────────

fn capital_from_cel(req: &CelBacktestRequest) -> CapitalConfig {
    CapitalConfig {
        initial:       req.initial_capital,
        position_pct:  req.position_size_pct,
        position_usd:  req.position_size_usd,
        max_positions: req.max_positions,
    }
}

fn execution_from_cel(req: &CelBacktestRequest) -> ExecutionConfig {
    ExecutionConfig {
        commission_pct:   req.commission_pct,
        slippage_pct:     req.slippage_pct,
        risk_free_annual: req.risk_free_annual,
    }
}

fn capital_from_rhai(req: &RhaiBacktestRequest) -> CapitalConfig {
    CapitalConfig {
        initial:       req.initial_capital,
        position_pct:  req.position_size_pct,
        position_usd:  req.position_size_usd,
        max_positions: req.max_positions,
    }
}

fn execution_from_rhai(req: &RhaiBacktestRequest) -> ExecutionConfig {
    ExecutionConfig {
        commission_pct:   req.commission_pct,
        slippage_pct:     req.slippage_pct,
        risk_free_annual: req.risk_free_annual,
    }
}

fn date_to_ms(s: &str) -> Option<i64> {
    NaiveDate::parse_from_str(s, "%Y-%m-%d").ok()
        .and_then(|d| d.and_hms_opt(0, 0, 0))
        .map(|dt| Utc.from_utc_datetime(&dt).timestamp_millis())
}

fn too_many_requests() -> Response {
    (
        StatusCode::TOO_MANY_REQUESTS,
        Json(ErrorResponse {
            error: "too many concurrent backtests — retry shortly".into(),
        }),
    )
        .into_response()
}

// Workaround: BacktestResponse is only referenced through `Json<>`. Silence
// the dead-import warning in case rustc cannot see through the `Json` layer.
#[allow(dead_code)]
fn _enforce_response_import(_r: BacktestResponse) {}
