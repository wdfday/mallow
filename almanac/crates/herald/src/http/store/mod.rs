//! Store module — CRUD handlers + domain types + backend dispatch.
//!
//! # Routes (registered in `http/mod.rs`)
//!
//! ```text
//! GET    /api/store/strategies                    list all versions
//! POST   /api/store/strategies                    create (or new version)
//! GET    /api/store/strategies/:id                get by UUID
//! GET    /api/store/strategies/:name/versions     all versions of a name
//! PUT    /api/store/strategies/:id                update label / notes
//! DELETE /api/store/strategies/:id                delete version
//!
//! GET    /api/store/cases                         list all
//! POST   /api/store/cases                         create
//! GET    /api/store/cases/:id                     get one
//! PUT    /api/store/cases/:id                     update fields
//! DELETE /api/store/cases/:id                     delete
//! POST   /api/store/cases/:id/run                 resolve + run → save result
//!
//! GET    /api/store/cases/:id/results             list results for case
//! GET    /api/store/results/:id                   get one result
//! DELETE /api/store/results/:id                   delete result
//! ```

pub mod backend;
pub mod handlers;
pub mod migrate;
pub mod types;

pub use backend::StoreBackend;
pub use handlers::*;
pub use types::*;

use axum::{
    routing::{get, post},
    Router,
};

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        // ── strategies ──────────────────────────────────────────────────────
        .route("/api/v1/store/strategies",
            get(list_strategies).post(create_strategy))
        .route("/api/v1/store/strategies/:id",
            get(get_strategy).put(update_strategy).delete(delete_strategy))
        .route("/api/v1/store/strategies/:name/versions",
            get(list_strategy_versions))
        // ── cases ───────────────────────────────────────────────────────────
        .route("/api/v1/store/cases",
            get(list_cases).post(create_case))
        .route("/api/v1/store/cases/:id",
            get(get_case).put(update_case).delete(delete_case))
        .route("/api/v1/store/cases/:id/run",     post(run_case))
        .route("/api/v1/store/cases/:id/signals", post(run_case_signals))
        .route("/api/v1/store/cases/:id/results", get(list_results))
        // ── results ─────────────────────────────────────────────────────────
        .route("/api/v1/store/results/:id",
            get(get_result).delete(delete_result))
}
