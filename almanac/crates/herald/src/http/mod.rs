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
//! | `GET  /health`               | Health check                                                                 |
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
//! | `POST /api/backtest/rhai`              | Run a Rhai-script strategy                      |
//!
//! ## Store — CRUD for saved strategies and backtest cases
//!
//! | Route                                  | Purpose                                         |
//! |----------------------------------------|-------------------------------------------------|
//! | `GET  /api/store/strategies`           | List all saved strategies                       |
//! | `POST /api/store/strategies`           | Create a saved strategy                         |
//! | `GET  /api/store/strategies/:id`       | Get one by id                                   |
//! | `PUT  /api/store/strategies/:id`       | Update label / notes                            |
//! | `DELETE /api/store/strategies/:id`     | Delete                                          |
//! | `GET  /api/store/strategies/:name/versions` | All versions for a name                   |
//! | `GET  /api/store/cases`                | List all backtest cases                         |
//! | `POST /api/store/cases`                | Create a backtest case (strategy_id required)   |
//! | `GET  /api/store/cases/:id`            | Get one by id                                   |
//! | `PUT  /api/store/cases/:id`            | Update fields                                   |
//! | `DELETE /api/store/cases/:id`          | Delete                                          |
//! | `POST /api/store/cases/:id/run`        | Resolve strategy + run backtest                 |
//! | `POST /api/store/cases/:id/signals`    | Replay signals (no SimBroker)                   |
//! | `GET  /api/store/cases/:id/results`    | List results for case                           |
//! | `GET  /api/store/results/:id`          | Get one result                                  |
//! | `DELETE /api/store/results/:id`        | Delete result                                   |
//!
//! ## Watch — signal dispatch without trade execution (noop storage)
//!
//! | Route                                  | Purpose                                         |
//! |----------------------------------------|-------------------------------------------------|
//! | `GET  /api/watch`                      | List all watch entries                          |
//! | `POST /api/watch`                      | Create a watch entry                            |
//! | `GET  /api/watch/:id`                  | Get one                                         |
//! | `DELETE /api/watch/:id`                | Remove                                          |
//!
//! ## Stream — SSE real-time push
//!
//! | Route                      | Purpose                                                     |
//! |----------------------------|-------------------------------------------------------------|
//! | `POST /api/stream/:symbol` | Bar + indicator events for one symbol (event: `"bar"`)      |
//! | `GET  /api/stream/signals` | All signal batches (event: `"signal"`)                      |
//!
//! `POST /api/stream/:symbol` body (`StreamRequest`):
//! - `tf`: timeframe string, e.g. `"M1"` (default = herald's global TF).
//! - `indicators`: structured list of indicator configs — raw cell values returned per bar.
//! - `script`: Rhai script using `ind.TYPE(period)` declarations + `plot("name", value)` calls.
//!   Exactly one of `indicators` or `script` should be set.
//!
//! **MTF note:** `tf` is a single timeframe that applies to both the bar feed and all
//! indicators. Per-indicator source-timeframe override (`source_tf`) is not yet
//! supported on the stream endpoint — use `POST /api/data/:symbol` for multi-TF snapshots.
//!
//! The first event on every connection is always `"status"` (`StreamStatus`), reporting
//! warm-up state per indicator before any `"bar"` event flows.
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
use std::sync::Arc;

use alm_core::Bar;
use alm_ledger::Ledger;
use axum::{routing::get, Json, Router};
use serde_json::json;
use tokio::sync::{broadcast, Semaphore};
use tower_http::compression::CompressionLayer;
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;

use crate::registry::HandSignal;
use crate::watch_evaluator::WatchEvaluator;

mod backtest;
pub mod data;
mod duckdb_helpers;
mod openapi;
mod sse;
mod rhai_validate;
mod symbols;
mod types;
pub mod store;
pub mod watch;

pub use openapi::ApiDoc;
pub use store::StoreBackend;
pub use watch::{new_store as new_watch_store, WatchStore};

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
    /// CRUD store for saved strategies + backtest cases.
    pub store: StoreBackend,
    /// In-memory watchlist store (noop — not yet wired to live bar pipeline).
    pub watches: WatchStore,
    /// Live bar broadcast — SSE `/api/stream/:symbol` subscribers receive from here.
    pub bar_bcast: broadcast::Sender<Bar>,
    /// Live signal broadcast — SSE `/api/stream/signals` subscribers receive from here.
    pub sig_bcast: broadcast::Sender<Arc<HandSignal>>,
    /// Watch evaluator — `delete_watch` calls `remove_watch` to eagerly free handles.
    pub watch_evaluator: Arc<WatchEvaluator>,
}

impl HttpState {
    pub fn new(
        ledger: Arc<Ledger>,
        tf: alm_core::Timeframe,
        data_dir: Arc<PathBuf>,
        max_concurrent_backtests: usize,
        store: StoreBackend,
        bar_bcast: broadcast::Sender<Bar>,
        sig_bcast: broadcast::Sender<Arc<HandSignal>>,
        watch_evaluator: Arc<WatchEvaluator>,
        watches: WatchStore,
    ) -> Self {
        Self {
            ledger,
            tf,
            data_dir,
            backtest_semaphore: Arc::new(Semaphore::new(max_concurrent_backtests.max(1))),
            store,
            watches,
            bar_bcast,
            sig_bcast,
            watch_evaluator,
        }
    }
}

pub fn router(state: HttpState) -> Router {
    Router::new()
        .route("/health", get(health))
        .merge(symbols::routes())
        .merge(data::routes())
        .merge(backtest::routes())
        .merge(rhai_validate::routes())
        .merge(store::routes())
        .merge(watch::routes())
        .merge(sse::routes())
        .merge(openapi::routes())
        .with_state(state)
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http())
        .layer(CompressionLayer::new())
}

async fn health() -> Json<serde_json::Value> {
    Json(json!({ "ok": true, "service": "herald" }))
}
