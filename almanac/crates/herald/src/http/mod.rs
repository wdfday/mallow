//! HTTP API for herald — single HTTP surface for both live ledger data and
//! batch backtest execution.
//!
//! # Design
//!
//! Herald owns the only HTTP server in `almanac`. Routes split into families:
//!
//! ## Live — backed by the in-process `alm_ledger::Ledger`
//!
//! | Route                                       | Purpose                                    |
//! |---------------------------------------------|--------------------------------------------|
//! | `GET  /health`                              | Health check                               |
//! | `GET  /api/symbols`                         | Symbols tracked by the ledger              |
//! | `GET  /api/symbols?indicators=true`         | Same + live indicator cells per symbol     |
//! | `GET  /api/indicators`                      | Indicator catalogue (static)               |
//! | `GET  /api/data/:symbol`                    | OHLCV window with cursor pagination        |
//! | `GET  /api/data/:symbol/latest`             | Shortcut for "last N bars"                 |
//! | `POST /api/data/:symbol`                    | Unified OHLCV + indicator snapshot         |
//! | `POST /api/data/duckdb`                     | Ad-hoc SQL over flat Parquet files         |
//!
//! ## Batch — dispatched to `alm_engine::backtest`
//!
//! | Route                                  | Purpose                                         |
//! |----------------------------------------|-------------------------------------------------|
//! | `GET  /api/strategies`                 | Registered named-strategy keys                  |
//! | `POST /api/backtest`                   | Run a named strategy                            |
//! | `POST /api/backtest/cel`               | Run a CEL-expression strategy                   |
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
//! | Route                                  | Purpose                                         |
//! |----------------------------------------|-------------------------------------------------|
//! | `GET  /api/stream/:symbol`             | Live bar events for one symbol (event: "bar")   |
//! | `GET  /api/stream/signals`             | All signal batches (event: "signal")            |
//!
//! # Cursor pagination
//!
//! `GET /api/data/:symbol` uses two cursors:
//!
//! - `?before=<ts_ms>&limit=N` — bars with `t < before`, newest-first, up to `N`.
//! - `?after=<ts_ms>&limit=N`  — bars with `t > after`, oldest-first, up to `N`.
//! - Neither → the last `N` bars (equivalent to `latest`).

use std::path::PathBuf;
use std::sync::Arc;

use alm_core::Bar;
use alm_ledger::Ledger;
use axum::{routing::get, Json, Router};
use serde_json::json;
use tokio::sync::{broadcast, Semaphore};
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;

use crate::registry::SignalBatch;

mod backtest;
pub mod data;
mod duckdb_helpers;
mod openapi;
mod sse;
mod symbols;
mod types;
pub mod store;
pub mod watch;

pub use openapi::ApiDoc;
pub use store::StoreBackend;
pub use watch::WatchStore;

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
    pub sig_bcast: broadcast::Sender<Arc<SignalBatch>>,
}

impl HttpState {
    pub fn new(
        ledger: Arc<Ledger>,
        tf: alm_core::Timeframe,
        data_dir: Arc<PathBuf>,
        max_concurrent_backtests: usize,
        store: StoreBackend,
        bar_bcast: broadcast::Sender<Bar>,
        sig_bcast: broadcast::Sender<Arc<SignalBatch>>,
    ) -> Self {
        Self {
            ledger,
            tf,
            data_dir,
            backtest_semaphore: Arc::new(Semaphore::new(max_concurrent_backtests.max(1))),
            store,
            watches: watch::new_store(),
            bar_bcast,
            sig_bcast,
        }
    }
}

pub fn router(state: HttpState) -> Router {
    Router::new()
        .route("/health", get(health))
        .merge(symbols::routes())
        .merge(data::routes())
        .merge(backtest::routes())
        .merge(store::routes())
        .merge(watch::routes())
        .merge(sse::routes())
        .merge(openapi::routes())
        .with_state(state)
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http())
}

async fn health() -> Json<serde_json::Value> {
    Json(json!({ "ok": true, "service": "herald" }))
}
