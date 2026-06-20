//! HTTP API for herald — single HTTP surface for both live ledger data and
//! batch backtest execution.
//!
//! # Design
//!
//! Herald owns the only HTTP server in `almanac`. Routes split into families:
//!
//! ## Live — backed by the in-process `alm_ledger::Ledger`
//!
//! | Route                        | Purpose                                                                      |
//! |------------------------------|------------------------------------------------------------------------------|
//! | `GET  /health`               | Liveness probe — always 200 while the process is alive                       |
//! | `GET  /ready`                | Readiness probe — 503 during gap-fill warmup, 200 once ready                 |
//! | `GET  /api/symbols`          | Symbols tracked by the ledger; `?indicators=true` adds live cells per symbol |
//! | `GET  /api/indicators`       | Indicator catalogue (static metadata)                                        |
//! | `POST /api/data/:symbol`     | OHLCV + indicator snapshot; transparent Parquet fallback for historical pages |
//!
//! ## Batch — dispatched to `alm_engine::backtest`
//!
//! | Route                                  | Purpose                                         |
//! |----------------------------------------|-------------------------------------------------|
//! | `GET  /api/strategies`                 | Registered named-strategy keys                  |
//! | `POST /api/backtest`                   | Run a named strategy                            |
//! | `POST /api/backtest/script`              | Run a script strategy                      |
//!
//! ## Store — CRUD for saved strategy versions
//!
//! | Route                                  | Purpose                                         |
//! |----------------------------------------|-------------------------------------------------|
//! | `GET  /api/store/strategies`           | List all saved strategies                       |
//! | `POST /api/store/strategies`           | Create a saved strategy                         |
//! | `GET  /api/store/strategies/:id`       | Get one by id                                   |
//! | `PUT  /api/store/strategies/:id`       | Update label / notes                            |
//! | `DELETE /api/store/strategies/:id`     | Delete                                          |
//! | `GET  /api/store/strategies/:name/versions` | All versions for a name                   |
//!
//! # Cursor pagination (`POST /api/data/:symbol`)
//!
//! Three mutually exclusive cursor modes in the `candles` section:
//!
//! - `before: <ts_ms>` — bars with `t < before`, newest `limit` of those, oldest-first.
//! - `after: <ts_ms>`  — bars with `t > after`, oldest `limit` of those, oldest-first.
//! - Neither           — the last `limit` bars (equivalent to "latest").
//!
//! When `before` predates the live ledger window **and** indicators are requested,
//! the handler runs a historical compute pass: it loads `warm_estimate + limit` bars
//! from Parquet, feeds them through fresh indicator instances, and returns only the
//! last `limit` bars with aligned indicator values. The Parquet scan uses DuckDB
//! row-group zone-map filtering (min/max statistics per row group) — no explicit
//! file index is needed.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use alm_ledger::Ledger;
use axum::{extract::State, http::StatusCode, middleware, response::IntoResponse, routing::get, Json, Router};
use metrics::{counter, histogram};
use metrics_exporter_prometheus::PrometheusHandle;
use serde_json::json;
use tokio::sync::Semaphore;
use tower_http::compression::CompressionLayer;
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;

use crate::ws_latency::WsLatencyTracker;

pub mod admin;
mod backtest;
pub mod data;
mod openapi;
mod script_validate;
pub mod store;
mod symbols;
mod types;
pub mod strategy;

pub use openapi::ApiDoc;
pub use store::StoreBackend;

#[cfg(test)]
mod tests;

/// Shared state handed to every HTTP handler.
#[derive(Clone)]
pub struct HttpState {
    pub ledger: Arc<Ledger>,
    /// Default timeframe when the request does not override it.
    pub tf: alm_core::Timeframe,
    /// Parquet root — backtest handlers load historical bars from here.
    pub data_dir: Arc<PathBuf>,
    /// Caps concurrent backtest runs across NATS + HTTP to protect RAM/CPU.
    pub backtest_semaphore: Arc<Semaphore>,
    /// CRUD store for saved strategy versions.
    pub store: StoreBackend,
    /// WebSocket delivery latency tracker — shared with `Handler`.
    pub ws_latency: Arc<WsLatencyTracker>,
    /// Prometheus metrics handle — rendered by `GET /metrics`.
    pub prometheus: PrometheusHandle,
    /// Set to true once bootstrap + gap-fill finish. /health returns 503 until then.
    pub ready: Arc<AtomicBool>,
}

impl HttpState {
    pub fn new(
        ledger: Arc<Ledger>,
        tf: alm_core::Timeframe,
        data_dir: Arc<PathBuf>,
        max_concurrent_backtests: usize,
        store: StoreBackend,
        ws_latency: Arc<WsLatencyTracker>,
        prometheus: PrometheusHandle,
    ) -> (Self, Arc<AtomicBool>) {
        let ready = Arc::new(AtomicBool::new(false));
        let state = Self {
            ledger,
            tf,
            data_dir,
            backtest_semaphore: Arc::new(Semaphore::new(max_concurrent_backtests.max(1))),
            store,
            ws_latency,
            prometheus,
            ready: Arc::clone(&ready),
        };
        (state, ready)
    }
}

async fn record_request_metrics(
    req: axum::http::Request<axum::body::Body>,
    next: middleware::Next,
) -> axum::response::Response {
    let method = req.method().to_string();
    let path = req.uri().path().to_string();
    let start = std::time::Instant::now();
    let res = next.run(req).await;
    let status = res.status().as_u16();
    let duration_ms = start.elapsed().as_millis() as f64;
    let status_str = status.to_string();
    counter!("herald_http_requests_total",
        "method" => method.clone(),
        "path"   => path.clone(),
        "status" => status_str,
    ).increment(1);
    histogram!("herald_http_request_duration_ms",
        "method" => method.clone(),
        "path"   => path.clone(),
    ).record(duration_ms);
    // /health and /ready are polled by Docker + monitoring every few seconds.
    // Log them at debug to avoid flooding WARN during normal warmup / polling.
    let is_probe = path == "/health" || path == "/ready";
    if status >= 500 && !is_probe {
        tracing::warn!(method, path, status, duration_ms, "http request");
    } else {
        tracing::debug!(method, path, status, duration_ms, "http request");
    }
    res
}

pub fn router(state: HttpState) -> Router {
    Router::new()
        .route("/health", get(health))
        .route("/ready", get(ready))
        .route("/metrics", get(prometheus_metrics))
        .merge(symbols::routes())
        .merge(data::routes())
        .merge(backtest::routes())
        .merge(script_validate::routes())
        .merge(strategy::routes())
        .merge(openapi::routes())
        .merge(admin::routes())
        .with_state(state)
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http())
        .layer(CompressionLayer::new())
        .layer(middleware::from_fn(record_request_metrics))
}

/// Liveness probe — always 200 while the process is alive.
/// Used by Docker healthcheck so the container is never killed during gap-fill.
async fn health() -> impl IntoResponse {
    (StatusCode::OK, Json(json!({ "ok": true, "service": "herald" })))
}

/// Readiness probe — 503 during bootstrap/gap-fill, 200 once ready.
/// Used by load balancers / API gateway to gate traffic.
async fn ready(State(state): State<HttpState>) -> impl IntoResponse {
    if state.ready.load(Ordering::Relaxed) {
        (StatusCode::OK, Json(json!({ "ok": true, "service": "herald" })))
    } else {
        (StatusCode::SERVICE_UNAVAILABLE, Json(json!({ "ok": false, "service": "herald", "msg": "warming up" })))
    }
}

async fn prometheus_metrics(State(state): State<HttpState>) -> String {
    state.prometheus.render()
}
