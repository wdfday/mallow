//! Backtest endpoints — thin adapters over [`alm_engine::backtest::run`].
//!
//! The heavy lifting (Parquet loading, engine loop, report aggregation) lives
//! in `alm-engine` as a plain library function. Herald's job here is limited to:
//!
//! - Serialising the request / response envelopes.
//! - Enforcing a concurrency cap via [`HttpState::backtest_semaphore`].
//! - Dispatching the CPU-bound engine run onto `spawn_blocking`.
//!
//! # Concurrency
//!
//! The semaphore is acquired with `try_acquire`; at-capacity requests receive
//! `429 Too Many Requests` instead of queueing. This keeps tail latency
//! bounded — the client can retry with back-off or switch to the NATS
//! queue (future).

use std::sync::Arc;

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
use tracing::{error, info, warn};

use axum::routing::{get, post};

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/strategies", get(list_strategies))
        .route("/api/backtest", post(run_backtest))
        .route("/api/backtest/cel", post(run_backtest_cel))
        .route("/api/backtest/rhai", post(run_backtest_rhai))
}

// ── GET /api/strategies ──────────────────────────────────────────────────────

#[utoipa::path(
    get,
    path = "/api/strategies",
    responses(
        (status = 200, description = "List of registered named strategy keys")
    ),
    tag = "backtest"
)]
pub async fn list_strategies() -> Json<&'static [&'static str]> {
    Json(STRATEGY_KEYS)
}

// ── POST /api/backtest ───────────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/backtest",
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
    dispatch(Arc::clone(&state.data_dir), req).await
}

// ── POST /api/backtest/cel ───────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/backtest/cel",
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
    info!(symbol = %req.symbol, "CEL backtest request");
    let base: BacktestRequest = req.into();
    dispatch(Arc::clone(&state.data_dir), base).await
}

// ── POST /api/backtest/rhai ──────────────────────────────────────────────────

#[utoipa::path(
    post,
    path = "/api/backtest/rhai",
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
    info!(symbol = %req.symbol, "Rhai backtest request");
    let base: BacktestRequest = req.into();
    dispatch(Arc::clone(&state.data_dir), base).await
}

// ── POST /api/backtest/dynamic — DEPRECATED ──────────────────────────────────
// pub async fn run_backtest_dynamic(...) { ... }  // use /api/backtest/cel instead

// ── Helpers ──────────────────────────────────────────────────────────────────

fn try_acquire(state: &HttpState) -> Option<tokio::sync::OwnedSemaphorePermit> {
    Arc::clone(&state.backtest_semaphore).try_acquire_owned().ok()
}

pub(super) async fn dispatch(data_dir: Arc<std::path::PathBuf>, req: BacktestRequest) -> Response {
    let result = tokio::task::spawn_blocking(move || backtest::run(req, &data_dir)).await;
    match result {
        Ok(Ok(resp)) => (StatusCode::OK, Json(resp)).into_response(),
        Ok(Err(e)) => {
            warn!(error = %e, "backtest failed");
            (
                StatusCode::BAD_REQUEST,
                Json(ErrorResponse { error: e.to_string() }),
            )
                .into_response()
        }
        Err(e) => {
            error!(error = %e, "backtest spawn_blocking panicked");
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(ErrorResponse { error: "internal engine error".into() }),
            )
                .into_response()
        }
    }
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
