//! Backtest endpoints — thin adapters over [`alm_engine::backtest::run`].
//!
//! The heavy lifting (Parquet loading, engine loop, report aggregation) lives
//! in `alm-engine` as a plain library function. Herald's job here is limited to:
//!
//! - Serialising the request / response envelopes.
//! - Enforcing a concurrency cap via [`HttpState::backtest_semaphore`].
//! - Dispatching the CPU-bound engine run onto `spawn_blocking`.
//!
//! `POST /api/v1/backtest/script` is a **pure run** — it does not save the
//! strategy. Use `POST /api/v1/strategy/strategies` to persist a version.
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
    types::{BacktestRequest, BacktestResponse, MtfBacktestRequest, ScriptBacktestRequest},
};
use metrics::{gauge, histogram};
use super::types::{ok, err, err_code};
use alm_strategy::catalog::STRATEGY_KEYS;
use alm_strategy::MTF_STRATEGY_KEYS;
use serde::Serialize;
use tracing::{error, info, warn};

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/strategies", get(list_strategies))
        .route("/api/v1/backtest", post(run_backtest))
        .route("/api/v1/backtest/estimate", post(estimate_backtest))
        .route("/api/v1/backtest/script", post(run_backtest_script))
        .route("/api/v1/backtest/mtf", post(run_backtest_mtf))
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

#[derive(Debug, serde::Serialize)]
struct StrategyListResponse {
    named: &'static [&'static str],
    mtf: &'static [&'static str],
}

#[utoipa::path(
    get,
    path = "/api/v1/strategies",
    responses(
        (status = 200, description = "Named and MTF strategy keys")
    ),
    tag = "backtest"
)]
pub async fn list_strategies() -> Response {
    ok(StrategyListResponse { named: STRATEGY_KEYS, mtf: MTF_STRATEGY_KEYS })
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
        (status = 200, description = "Script backtest report"),
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

    info!(symbol = %req.symbol, "Script backtest request");

    let started_at = std::time::Instant::now();
    let base: BacktestRequest = req.into();
    let report = match run_blocking(Arc::clone(&state.data_dir), base).await {
        Ok(r)  => r,
        Err(r) => return r,
    };
    let elapsed_ms = started_at.elapsed().as_millis() as u64;

    info!(
        elapsed_ms,
        total_return = report.returns.total_pct,
        sharpe       = report.risk_adjusted.sharpe,
        trades       = report.trade_stats.total,
        "script backtest done"
    );

    let mut body = serde_json::to_value(&report).unwrap_or_default();
    body["elapsed_ms"] = serde_json::json!(elapsed_ms);
    ok(body)
}

// ── POST /api/backtest/mtf ───────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/v1/backtest/mtf",
    responses(
        (status = 200, description = "MTF backtest report"),
        (status = 400, description = "Bad request"),
        (status = 429, description = "Too many concurrent backtests")
    ),
    tag = "backtest"
)]
pub async fn run_backtest_mtf(
    State(state): State<HttpState>,
    Json(req): Json<MtfBacktestRequest>,
) -> Response {
    let Some(_permit) = try_acquire(&state) else {
        return too_many_requests();
    };
    info!(
        strategy = %req.strategy,
        symbol   = %req.symbol,
        base_tf  = req.base_tf.as_deref().unwrap_or("M1"),
        htf      = ?req.htf_timeframes,
        "HTTP MTF backtest request",
    );
    const BACKTEST_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(300);
    let data_dir = Arc::clone(&state.data_dir);
    let t0 = std::time::Instant::now();
    let task = tokio::task::spawn_blocking(move || backtest::run_mtf(req, &data_dir));
    let result = tokio::time::timeout(BACKTEST_TIMEOUT, task).await;
    let elapsed_ms = t0.elapsed().as_millis() as f64;
    match result {
        Err(_elapsed) => {
            warn!(elapsed_ms, timeout_secs = 300u64, "MTF backtest timed out");
            metrics::counter!("herald_backtest_timeouts_total").increment(1);
            err(StatusCode::REQUEST_TIMEOUT, "backtest timed out — reduce date range or try again")
        }
        Ok(Ok(Ok(report))) => {
            histogram!("herald_backtest_duration_ms").record(elapsed_ms);
            ok(report)
        }
        Ok(Ok(Err(e))) => {
            warn!(error = %e, "MTF backtest failed");
            err(StatusCode::BAD_REQUEST, e.to_string())
        }
        Ok(Err(e)) => {
            error!(error = %e, "MTF backtest spawn_blocking panicked");
            err(StatusCode::INTERNAL_SERVER_ERROR, "internal engine error")
        }
    }
}

// ── Core helpers ─────────────────────────────────────────────────────────────

struct BacktestPermit(#[allow(dead_code)] tokio::sync::OwnedSemaphorePermit);

impl Drop for BacktestPermit {
    fn drop(&mut self) {
        gauge!("herald_backtest_inflight").decrement(1.0);
    }
}

fn try_acquire(state: &HttpState) -> Option<BacktestPermit> {
    Arc::clone(&state.backtest_semaphore)
        .try_acquire_owned()
        .ok()
        .map(|p| {
            gauge!("herald_backtest_inflight").increment(1.0);
            BacktestPermit(p)
        })
}

/// Run the engine on a blocking thread. Returns `Ok(report)` or `Err(error_response)`.
/// Times out after 300 s — an infinite Rhai loop won't block the server forever.
pub(super) async fn run_blocking(
    data_dir: Arc<std::path::PathBuf>,
    req: BacktestRequest,
) -> Result<BacktestResponse, Response> {
    const BACKTEST_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(300);
    let t0 = std::time::Instant::now();
    let task = tokio::task::spawn_blocking(move || backtest::run(req, &data_dir));
    let result = tokio::time::timeout(BACKTEST_TIMEOUT, task).await;
    let elapsed_ms = t0.elapsed().as_millis() as f64;
    match result {
        Err(_elapsed) => {
            warn!(elapsed_ms, timeout_secs = 300u64, "backtest timed out");
            metrics::counter!("herald_backtest_timeouts_total").increment(1);
            Err(err(StatusCode::REQUEST_TIMEOUT, "backtest timed out — reduce date range or try again"))
        }
        Ok(Ok(Ok(resp))) => {
            histogram!("herald_backtest_duration_ms").record(elapsed_ms);
            Ok(resp)
        }
        Ok(Ok(Err(e))) => {
            warn!(error = %e, "backtest failed");
            Err(err(StatusCode::BAD_REQUEST, e.to_string()))
        }
        Ok(Err(e)) => {
            error!(error = %e, "backtest spawn_blocking panicked");
            Err(err(StatusCode::INTERNAL_SERVER_ERROR, "internal engine error"))
        }
    }
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
