//! Backtest endpoints — thin adapters over [`alm_engine::backtest::run`].
//!
//! The heavy lifting (Parquet loading, engine loop, report aggregation) lives
//! in `alm-engine` as a plain library function. Herald's job here is limited to:
//!
//! - Serialising the request / response envelopes.
//! - Enforcing a concurrency cap via [`HttpState::backtest_semaphore`].
//! - Dispatching the CPU-bound engine run onto `spawn_blocking`.
//! - Persisting strategy + case + result on every `POST /api/v1/backtest/script`.
//!
//! # Always-persist flow (`POST /api/v1/backtest/script`)
//!
//! Requests that reach this endpoint are "deep backtests" — too large for the
//! in-browser WASM runner. Every such request is worth keeping:
//!
//! 1. Validate → acquire semaphore.
//! 2. Upsert strategy by `spec_hash` (same script → existing version returned).
//! 3. Upsert case: `case_id` present → UPDATE that row; absent → CREATE new row.
//! 4. Run engine on `spawn_blocking`.
//! 5. Save `BacktestResult` row.
//! 6. Return merged response: `{ ...report, saved: { strategy_id, case_id, result_id } }`.
//!
//! # Concurrency
//!
//! The semaphore is acquired with `try_acquire`; at-capacity requests receive
//! `429 Too Many Requests` instead of queueing. This keeps tail latency
//! bounded — the client can retry with back-off.

use std::sync::Arc;

use axum::{
    extract::State,
    http::StatusCode,
    response::Response,
    Json, Router,
};
use axum::routing::{get, post};
use alm_engine::{
    backtest,
    types::{BacktestRequest, BacktestResponse, ScriptBacktestRequest},
};
use super::types::{ok, err, err_code};
use alm_strategy::catalog::STRATEGY_KEYS;
use chrono::{NaiveDate, TimeZone, Utc};
use serde::Serialize;
use tracing::{error, info, warn};

use super::store::types::{PositionConfig, ExecutionConfig, StrategySpec};
use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/strategies", get(list_strategies))
        .route("/api/v1/backtest", post(run_backtest))
        .route("/api/v1/backtest/estimate", post(estimate_backtest))
        .route("/api/v1/backtest/script", post(run_backtest_script))
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
pub async fn list_strategies() -> Response {
    ok(STRATEGY_KEYS)
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
            ok(EstimateResponse {
                bar_count,
                estimated_seconds,
                within_limit: bar_count <= MAX_BARS,
                limit: MAX_BARS,
            })
        }
        Ok(Err(e)) => {
            warn!(error = %e, "estimate failed");
            err(StatusCode::BAD_REQUEST, e.to_string())
        }
        Err(e) => {
            error!(error = %e, "estimate spawn_blocking panicked");
            err(StatusCode::INTERNAL_SERVER_ERROR, "internal error")
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
        Ok(report) => ok(report),
        Err(r) => r,
    }
}

// ── POST /api/backtest/script ──────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest/script",
    responses(
        (status = 200, description = "Script backtest report with saved IDs"),
        (status = 400, description = "Bad request"),
        (status = 429, description = "Too many concurrent backtests")
    ),
    tag = "backtest"
)]
pub async fn run_backtest_script(
    State(state): State<HttpState>,
    Json(req): Json<ScriptBacktestRequest>,
) -> Response {
    let Some(_permit) = try_acquire(&state) else {
        return too_many_requests();
    };

    let name      = req.name.clone();
    let spec      = StrategySpec { script: req.script.clone() };
    let symbol    = req.symbol.clone();
    let timeframe = Some(req.timeframe.clone().unwrap_or_else(|| state.tf.to_string()));
    let label     = req.label.clone().unwrap_or_else(|| format!("{name} on {symbol}"));
    let case_id   = req.case_id.clone();
    let from      = req.from.clone();
    let to        = req.to.clone();
    let capital   = capital_from_script(&req);
    let execution = execution_from_script(&req);
    let data_source = req.data_source.clone();
    let notes     = req.notes.clone();

    info!(symbol = %symbol, name = %name, ?case_id, "Script backtest request");

    // 1. Upsert strategy (dedup by spec_hash or compare against strategy_id).
    let strategy_id_hint = req.strategy_id.clone();
    let strategy = match state.store.upsert_strategy(name, label.clone(), spec, notes, strategy_id_hint).await {
        Ok(s)  => s,
        Err(e) => {
            warn!(error = %e, "strategy upsert failed");
            return err(StatusCode::INTERNAL_SERVER_ERROR, format!("strategy upsert: {e}"));
        }
    };

    // 2. Upsert case (C if no case_id; U if case_id provided).
    let case = match state.store.upsert_case(
        case_id, strategy.id.clone(), label,
        symbol.clone(), timeframe.clone(),
        from.as_deref().and_then(date_to_ms),
        to.as_deref().and_then(date_to_ms),
        data_source, capital, execution,
    ).await {
        Ok(c)  => c,
        Err(e) => {
            warn!(error = %e, "case upsert failed");
            return err(StatusCode::BAD_REQUEST, format!("case upsert: {e}"));
        }
    };

    // 3. Run engine.
    let base: BacktestRequest = req.into();
    let report = match run_blocking(Arc::clone(&state.data_dir), base).await {
        Ok(r)  => r,
        Err(r) => return r,
    };

    // 4. Save result.
    let ran_at = Utc::now().timestamp_millis();
    let result = match state.store.save_result(
        case.id.clone(), ran_at, None,
        report.returns.total_pct,
        report.risk_adjusted.sharpe,
        report.drawdown.max_pct,
        report.trade_stats.win_rate_pct,
        report.trade_stats.total as i64,
    ).await {
        Ok(r)  => r,
        Err(e) => {
            warn!(error = %e, case_id = %case.id, "failed to save result row — report still returned");
            // Return report anyway; log the save failure.
            let mut body = serde_json::to_value(&report).unwrap_or_default();
            body["saved_error"] = serde_json::Value::String(e.to_string());
            return ok(body);
        }
    };

    info!(
        strategy_id = %strategy.id,
        case_id      = %case.id,
        result_id    = %result.id,
        total_return = report.returns.total_pct,
        sharpe       = report.risk_adjusted.sharpe,
        trades       = report.trade_stats.total,
        "backtest done"
    );

    // 5. Merge `saved` into response JSON.
    let mut body = serde_json::to_value(&report).unwrap_or_default();
    body["saved"] = serde_json::json!({
        "strategy_id": strategy.id,
        "case_id":     case.id,
        "result_id":   result.id,
    });
    ok(body)
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
            Err(err(StatusCode::BAD_REQUEST, e.to_string()))
        }
        Err(e) => {
            error!(error = %e, "backtest spawn_blocking panicked");
            Err(err(StatusCode::INTERNAL_SERVER_ERROR, "internal engine error"))
        }
    }
}

fn capital_from_script(req: &ScriptBacktestRequest) -> PositionConfig {
    PositionConfig {
        initial:       req.initial_capital,
        position_pct:  req.position_size_pct,
        position_usd:  req.position_size_usd,
        max_positions: req.max_positions,
    }
}

fn execution_from_script(req: &ScriptBacktestRequest) -> ExecutionConfig {
    ExecutionConfig {
        commission_pct:   req.commission_pct,
        slippage_pct:     req.slippage_pct,
        risk_free_annual: req.risk_free_annual,
        intra_bar_mode:   req.intra_bar_mode.clone(),
    }
}

fn date_to_ms(s: &str) -> Option<i64> {
    NaiveDate::parse_from_str(s, "%Y-%m-%d").ok()
        .and_then(|d| d.and_hms_opt(0, 0, 0))
        .map(|dt| Utc.from_utc_datetime(&dt).timestamp_millis())
}

fn too_many_requests() -> Response {
    err_code(
        StatusCode::TOO_MANY_REQUESTS,
        "TOO_MANY_REQUESTS",
        "too many concurrent backtests — retry shortly",
    )
}

// Workaround: BacktestResponse is only referenced through `Json<>`. Silence
// the dead-import warning in case rustc cannot see through the `Json` layer.
#[allow(dead_code)]
fn _enforce_response_import(_r: BacktestResponse) {}
