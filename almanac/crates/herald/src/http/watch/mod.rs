//! Watch module — admin-managed warm-set + live signal dispatch.
//!
//! # Dual purpose
//!
//! 1. **Ledger warm-set**: creating a watch entry pins the indicator
//!    dependencies declared in its strategy spec into the ledger for every
//!    listed symbol.  Deleting the entry drops the `IndicatorHandle`s,
//!    decrementing refcounts and scheduling grace eviction when they reach 0.
//!
//! 2. **Signal dispatch** (future): the same entry will drive live bar
//!    evaluation and forward signals to `webhook_url` / `nats_subject`.
//!
//! # Routes
//!
//! ```text
//! GET    /api/v1/watch            list all watch entries
//! POST   /api/v1/watch            create entry + pin indicators in ledger
//! GET    /api/v1/watch/:id        get one
//! PUT    /api/v1/watch/:id        update (re-pins on spec/symbol change)
//! DELETE /api/v1/watch/:id        remove + release indicator handles
//! ```

pub mod handlers;
pub mod types;

pub use handlers::*;
pub use types::{new_store, CreateWatchReq, UpdateWatchReq, WatchEntry, WatchSlot, WatchStore};

use axum::{routing::get, Router};
use crate::http::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/watch", get(handlers::list_watches).post(handlers::create_watch))
        .route("/api/v1/watch/:id",
            get(handlers::get_watch)
                .put(handlers::update_watch)
                .delete(handlers::delete_watch))
}
