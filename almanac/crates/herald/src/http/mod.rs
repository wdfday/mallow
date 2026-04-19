//! HTTP API for herald — single HTTP surface for both live ledger data and
//! batch backtest execution.
//!
//! # Design
//!
//! Herald owns the only HTTP server in `almanac`. Routes split into two
//! families:
//!
//! ## Live — backed by the in-process `alm_ledger::Ledger`
//!
//! | Route                                  | Purpose                                         |
//! |----------------------------------------|-------------------------------------------------|
//! | `GET  /health`                         | Health check                                    |
//! | `GET  /api/symbols`                    | Symbols currently tracked by the ledger         |
//! | `GET  /api/indicators`                 | Indicator catalogue (static)                    |
//! | `GET  /api/indicators/live`            | Indicator cells currently in the ledger         |
//! | `GET  /api/data/:symbol`               | OHLCV window with cursor pagination             |
//! | `GET  /api/data/:symbol/latest`        | Shortcut for "last N bars"                      |
//! | `POST /api/data/:symbol`               | Unified OHLCV + indicator snapshot              |
//!
//! ## Batch — dispatched to `alm_engine::backtest`
//!
//! | Route                                  | Purpose                                         |
//! |----------------------------------------|-------------------------------------------------|
//! | `GET  /api/strategies`                 | Registered named-strategy keys                  |
//! | `POST /api/backtest`                   | Run a named strategy                            |
//! | `POST /api/backtest/cel`               | Run a CEL-expression strategy                   |
//! | `POST /api/backtest/dynamic`           | Run a JSON-declared dynamic strategy            |
//!
//! Backtests run on a blocking thread pool and are capped by a semaphore
//! (`HERALD_MAX_BACKTESTS`, default 4). Over-capacity requests get `429`.
//!
//! # Cursor pagination
//!
//! `GET /api/data/:symbol` uses two cursors:
//!
//! - `?before=<ts_ms>&limit=N` — bars with `t < before`, newest-first, up to `N`.
//! - `?after=<ts_ms>&limit=N`  — bars with `t > after`, oldest-first, up to `N`.
//! - Neither → the last `N` bars (equivalent to `latest`).
//!
//! Both cursors filter against the ledger's `bar_window` only; when the range
//! falls outside the window the handler sets `truncated_below=true` so the
//! client can route subsequent scrolls to cold storage (walk-back LRU — future work).

use std::path::PathBuf;
use std::sync::Arc;

use alm_ledger::Ledger;
use axum::{
    routing::{get, post},
    Json, Router,
};
use serde_json::json;
use tokio::sync::Semaphore;
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;

mod backtest;
mod data;
mod indicators;
mod symbols;
mod types;

/// Shared state handed to every HTTP handler.
#[derive(Clone)]
pub struct HttpState {
    pub ledger: Arc<Ledger>,
    /// Default timeframe when the request does not override it. Matches the
    /// single timeframe Herald currently processes live bars on.
    pub tf: alm_core::Timeframe,
    /// Parquet root — backtest handlers load historical bars from here.
    pub data_dir: Arc<PathBuf>,
    /// Caps concurrent backtest runs across NATS + HTTP to protect RAM/CPU.
    pub backtest_semaphore: Arc<Semaphore>,
}

impl HttpState {
    pub fn new(
        ledger: Arc<Ledger>,
        tf: alm_core::Timeframe,
        data_dir: Arc<PathBuf>,
        max_concurrent_backtests: usize,
    ) -> Self {
        Self {
            ledger,
            tf,
            data_dir,
            backtest_semaphore: Arc::new(Semaphore::new(max_concurrent_backtests.max(1))),
        }
    }
}

pub fn router(state: HttpState) -> Router {
    Router::new()
        // ── Live ────────────────────────────────────────────────────────
        .route("/health", get(health))
        .route("/api/symbols", get(symbols::list_symbols))
        .route("/api/indicators", get(indicators::list_indicators_catalog))
        .route("/api/indicators/live", get(indicators::list_indicators_live))
        .route("/api/data/{symbol}", get(data::get_data).post(data::unified_data))
        .route("/api/data/{symbol}/latest", get(data::get_latest))
        // ── Batch ───────────────────────────────────────────────────────
        .route("/api/strategies", get(backtest::list_strategies))
        .route("/api/backtest", post(backtest::run_backtest))
        .route("/api/backtest/cel", post(backtest::run_backtest_cel))
        .route("/api/backtest/dynamic", post(backtest::run_backtest_dynamic))
        .with_state(state)
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http())
}

async fn health() -> Json<serde_json::Value> {
    Json(json!({ "ok": true, "service": "herald" }))
}
