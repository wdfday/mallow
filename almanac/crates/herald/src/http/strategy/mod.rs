//! Store module — strategy CRUD handlers + domain types + backend dispatch.
//!
//! # Routes (registered in `http/mod.rs`)
//!
//! ```text
//! GET    /api/v1/strategy/strategies                    list all versions
//! POST   /api/v1/strategy/strategies                    create (or new version)
//! GET    /api/v1/strategy/my                            all versions grouped by name (caller only)
//! GET    /api/v1/strategy/strategies/:id                get by UUID
//! PUT    /api/v1/strategy/strategies/:id                update label / notes
//! DELETE /api/v1/strategy/strategies/:id                delete version
//! ```

pub mod handlers;
pub mod types;

pub use crate::http::store::StoreBackend;
pub use handlers::*;
pub use types::*;

use axum::{
    routing::get,
    Router,
};

use super::HttpState;

pub fn routes() -> Router<HttpState> {
    Router::new()
        .route("/api/v1/strategy/strategies",
            get(list_strategies).post(create_strategy))
        .route("/api/v1/strategy/my",
            get(list_my_strategies))
        .route("/api/v1/strategy/strategies/:id",
            get(get_strategy).put(update_strategy).delete(delete_strategy))
}
